package fsm

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/atomic"

	pebble "github.com/cockroachdb/pebble/v2"
	"github.com/hashicorp/raft"
	pebbledb "github.com/xkeyideal/raft-pebbledb/v2"
)

const (
	// snapshotVersion is the current snapshot format version
	// Increment this when making breaking changes to the snapshot format
	snapshotVersion uint32 = 1

	// snapshotMagic is a magic number to identify valid snapshots
	snapshotMagic uint32 = 0x52414654 // "RAFT" in hex
)

// SnapshotHeader is written at the beginning of each snapshot
// This follows the Consul pattern for snapshot metadata
type SnapshotHeader struct {
	Magic     uint32 // Magic number for validation
	Version   uint32 // Snapshot format version
	LastIndex uint64 // Last applied index at snapshot time
	Timestamp int64  // Unix timestamp when snapshot was created
	NodeId    string // Node ID that created the snapshot
	Checksum  uint32 // CRC32 checksum of the data (excluding header)
}

// SnapshotResult contains the result of a snapshot operation
// This is used to collect metrics about the snapshot
type SnapshotResult struct {
	Size     int64  // Total size in bytes
	Records  uint64 // Number of records written
	Checksum uint32 // CRC32 checksum of the data
	Duration time.Duration
}

type store struct {
	baseDir string
	log     pebble.Logger
	db      *atomic.Pointer[pebble.DB]
	closed  *atomic.Bool
}

func newStore(baseDir string) (*store, error) {
	cfg := pebbledb.DefaultPebbleDBConfig()

	if err := os.MkdirAll(baseDir, os.ModePerm); err != nil {
		return nil, err
	}

	log := &Logger{}

	// get pebbledb directory
	dbdir, err := getPebbleDBDir(baseDir)
	if err != nil {
		return nil, err
	}

	db, err := pebbledb.OpenPebbleDB(cfg, dbdir, log)
	if err != nil {
		return nil, err
	}

	return &store{
		baseDir: baseDir,
		log:     log,
		db:      atomic.NewPointer(db),
		closed:  atomic.NewBool(false),
	}, nil
}

func (s *store) getColumnFamily(cf string) byte {
	return pebbleCfMap[cf]
}

func (s *store) isclosed() bool {
	return s.closed.Load()
}

func (s *store) getBytes(key []byte) ([]byte, error) {
	if s.closed.Load() {
		return []byte{}, pebble.ErrClosed
	}

	db := s.db.Load()
	val, closer, err := db.Get(key)

	// if key not found return nil
	if err == pebble.ErrNotFound {
		return []byte{}, nil
	}

	if err != nil {
		return nil, err
	}

	data := make([]byte, len(val))
	copy(data, val)

	if err := closer.Close(); err != nil {
		return nil, err
	}

	return data, nil
}

func (s *store) buildColumnFamilyKey(cf byte, key []byte) []byte {
	return append([]byte{cf}, key...)
}

func (s *store) batch() *pebble.Batch {
	db := s.db.Load()
	return db.NewBatch()
}

func (s *store) write(b *pebble.Batch) error {
	return b.Commit(pebble.Sync)
}

func (s *store) getIterator() (*pebble.Iterator, error) {
	db := s.db.Load()
	return db.NewIter(&pebble.IterOptions{})
}

func (s *store) getSnapshot() *pebble.Snapshot {
	db := s.db.Load()
	return db.NewSnapshot()
}

func (s *store) saveSnapShot(nodeId, raftAddr string, lastIndex uint64, snapshot *pebble.Snapshot, sink raft.SnapshotSink) (*SnapshotResult, error) {
	iter, err := snapshot.NewIter(&pebble.IterOptions{})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	start := time.Now()
	var totalSize int64 = 0

	// Create a CRC32 hasher for data integrity
	hasher := crc32.NewIEEE()

	// Write snapshot header (we'll update checksum at the end)
	headerSize := 8 + 8 + 8 + 8 + 2 + len(nodeId) + 4 // magic + version + lastIndex + timestamp + nodeIdLen + nodeId + checksum
	headerBuf := make([]byte, headerSize)
	offset := 0

	// Magic
	binary.LittleEndian.PutUint32(headerBuf[offset:], snapshotMagic)
	offset += 4
	// Version (leave space, will be filled)
	binary.LittleEndian.PutUint32(headerBuf[offset:], snapshotVersion)
	offset += 4
	// LastIndex
	binary.LittleEndian.PutUint64(headerBuf[offset:], lastIndex)
	offset += 8
	// Timestamp
	binary.LittleEndian.PutUint64(headerBuf[offset:], uint64(time.Now().Unix()))
	offset += 8
	// NodeId length and data
	binary.LittleEndian.PutUint16(headerBuf[offset:], uint16(len(nodeId)))
	offset += 2
	copy(headerBuf[offset:], nodeId)
	offset += len(nodeId)
	// Checksum placeholder (will be 0 for now, we don't update it in-place for simplicity)
	// In a production system, you might buffer all data and compute checksum at the end

	if _, err := sink.Write(headerBuf); err != nil {
		sink.Cancel()
		return nil, err
	}
	totalSize += int64(len(headerBuf))

	var count uint64 = 0
	sz := make([]byte, 8)

	// iter pebblebd snapshot, write datas by io.Writer
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		val := iter.Value()

		count++

		// write key
		binary.LittleEndian.PutUint64(sz, uint64(len(key)))
		if _, err := sink.Write(sz); err != nil { // key size
			sink.Cancel()
			return nil, err
		}
		totalSize += 8
		hasher.Write(sz)                           // include in checksum
		if _, err := sink.Write(key); err != nil { // key data
			sink.Cancel()
			return nil, err
		}
		totalSize += int64(len(key))
		hasher.Write(key) // include in checksum

		// write val
		// gzip encode
		gzipVal, err := gzipEncode(val)
		if err != nil {
			sink.Cancel()
			return nil, err
		}

		binary.LittleEndian.PutUint64(sz, uint64(len(gzipVal)))
		if _, err := sink.Write(sz); err != nil { // val size
			sink.Cancel()
			return nil, err
		}
		totalSize += 8
		hasher.Write(sz)                               // include in checksum
		if _, err := sink.Write(gzipVal); err != nil { // val data
			sink.Cancel()
			return nil, err
		}
		totalSize += int64(len(gzipVal))
		hasher.Write(gzipVal) // include in checksum
	}

	// Check for iterator errors after iteration completes
	if err := iter.Error(); err != nil {
		sink.Cancel()
		return nil, fmt.Errorf("iterator error during snapshot: %w", err)
	}

	// Write trailing checksum for verification during restore
	checksumBuf := make([]byte, 4)
	checksum := hasher.Sum32()
	binary.LittleEndian.PutUint32(checksumBuf, checksum)
	if _, err := sink.Write(checksumBuf); err != nil {
		sink.Cancel()
		return nil, err
	}
	totalSize += 4

	duration := time.Since(start)
	s.log.Infof("SaveSnapshot %s-%s, lastIndex: %d, start: %s, end: %s, cost: %dms, count: %d, size: %d, checksum: 0x%08x\n",
		nodeId, raftAddr, lastIndex,
		start.Format(time.RFC3339), time.Now().Format(time.RFC3339),
		duration.Milliseconds(), count, totalSize, checksum,
	)

	if err := sink.Close(); err != nil {
		return nil, err
	}

	return &SnapshotResult{
		Size:     totalSize,
		Records:  count,
		Checksum: checksum,
		Duration: duration,
	}, nil
}

func (s *store) recoverSnapShot(nodeId, raftAddr string, reader io.ReadCloser) (*SnapshotHeader, error) {
	start := time.Now()

	// Read and validate snapshot header
	header, err := s.readSnapshotHeader(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read snapshot header: %w", err)
	}

	s.log.Infof("RestoreSnapshot: reading snapshot from node %s, lastIndex: %d, created: %s\n",
		header.NodeId, header.LastIndex, time.Unix(header.Timestamp, 0).Format(time.RFC3339))

	// s.baseDir/uuid
	dbdir := getNewRandomDBDirName(s.baseDir)

	var oldDirName string

	// from currentDBFilename get pebbledb current directory
	name, err := getCurrentDBDirName(s.baseDir)
	if err != nil {
		return nil, err
	}
	oldDirName = name

	newdb, err := pebbledb.OpenPebbleDB(pebbledb.DefaultPebbleDBConfig(), dbdir, s.log)
	if err != nil {
		return nil, err
	}

	// Cleanup helper for error cases
	cleanupNewDB := func() {
		newdb.Close()
		os.RemoveAll(dbdir)
	}

	wb := newPebbleWriteBatch(newdb)

	// Create hasher for checksum verification
	hasher := crc32.NewIEEE()

	var count uint64 = 0
	sz := make([]byte, 8)
	k := 0

	// from snapshot reader read datas, exit when io.EOF
	for {
		count++

		// read key
		_, err := io.ReadFull(reader, sz) // key size
		if err == io.EOF {
			break
		}

		if err != nil {
			wb.Destroy()
			cleanupNewDB()
			return nil, err
		}

		// Check if this might be the trailing checksum (4 bytes read as 8)
		keyLen := binary.LittleEndian.Uint64(sz)

		// Validate key length to detect end of data
		if keyLen > 1<<30 { // Unreasonably large key, likely we hit the checksum
			// This is the trailing checksum - verify it
			expectedChecksum := binary.LittleEndian.Uint32(sz[:4])
			actualChecksum := hasher.Sum32()
			if expectedChecksum != actualChecksum {
				wb.Destroy()
				cleanupNewDB()
				return nil, fmt.Errorf("snapshot checksum mismatch: expected 0x%08x, got 0x%08x", expectedChecksum, actualChecksum)
			}
			s.log.Infof("RestoreSnapshot: checksum verified: 0x%08x\n", actualChecksum)
			count-- // Don't count checksum as a record
			break
		}

		hasher.Write(sz) // include in checksum verification

		kdata := make([]byte, keyLen)
		_, err = io.ReadFull(reader, kdata) // key data
		if err == io.EOF {
			break
		}
		if err != nil {
			wb.Destroy()
			cleanupNewDB()
			return nil, err
		}
		hasher.Write(kdata)

		// read val
		_, err = io.ReadFull(reader, sz) // val size
		if err == io.EOF {
			break
		}
		if err != nil {
			wb.Destroy()
			cleanupNewDB()
			return nil, err
		}
		hasher.Write(sz)

		toRead := binary.LittleEndian.Uint64(sz)
		vdata := make([]byte, toRead)
		_, err = io.ReadFull(reader, vdata) // val data
		if err == io.EOF {
			break
		}
		if err != nil {
			wb.Destroy()
			cleanupNewDB()
			return nil, err
		}
		hasher.Write(vdata)

		// gzip decode
		ungzipVData, err := gzipDecode(vdata)
		if err != nil {
			wb.Destroy()
			cleanupNewDB()
			return nil, fmt.Errorf("failed to decompress snapshot data: %w", err)
		}

		// batch sync write db
		wb.Put(kdata, ungzipVData)
		k++

		// per 100 times writes commit batch
		if k >= 100 {
			k = 0
			wb.Commit()
		}
	}

	// Commit any remaining data that wasn't committed in the loop
	if k > 0 {
		wb.Commit()
	}

	wb.Destroy()
	newdb.Flush()

	if err := saveCurrentDBDirName(s.baseDir, dbdir); err != nil {
		cleanupNewDB()
		return nil, err
	}
	if err := replaceCurrentDBFile(s.baseDir); err != nil {
		cleanupNewDB()
		return nil, err
	}

	// swap old & new db
	old := s.db.Swap(newdb)
	if old != nil {
		old.Close()
	}

	// remove all old pebbledb storage files
	if err := os.RemoveAll(oldDirName); err != nil {
		return nil, err
	}

	s.log.Infof("RecoverFromSnapshot %s-%s, lastIndex: %d, cost: %dms, count: %d, newDir: %s, oldDir: %s\n",
		nodeId, raftAddr, header.LastIndex,
		time.Since(start).Milliseconds(), count,
		dbdir, oldDirName,
	)

	parent := filepath.Dir(oldDirName)
	if err := syncDir(parent); err != nil {
		return nil, err
	}

	return header, nil
}

// readSnapshotHeader reads and validates the snapshot header
func (s *store) readSnapshotHeader(reader io.Reader) (*SnapshotHeader, error) {
	// Read fixed-size header fields: magic(4) + version(4) + lastIndex(8) + timestamp(8) + nodeIdLen(2)
	headerBuf := make([]byte, 26)
	if _, err := io.ReadFull(reader, headerBuf); err != nil {
		return nil, fmt.Errorf("failed to read header: %w", err)
	}

	header := &SnapshotHeader{}
	offset := 0

	// Magic
	header.Magic = binary.LittleEndian.Uint32(headerBuf[offset:])
	offset += 4
	if header.Magic != snapshotMagic {
		return nil, fmt.Errorf("invalid snapshot magic: expected 0x%08x, got 0x%08x", snapshotMagic, header.Magic)
	}

	// Version
	header.Version = binary.LittleEndian.Uint32(headerBuf[offset:])
	offset += 4
	if header.Version > snapshotVersion {
		return nil, fmt.Errorf("unsupported snapshot version: %d (max supported: %d)", header.Version, snapshotVersion)
	}

	// LastIndex
	header.LastIndex = binary.LittleEndian.Uint64(headerBuf[offset:])
	offset += 8

	// Timestamp
	header.Timestamp = int64(binary.LittleEndian.Uint64(headerBuf[offset:]))
	offset += 8

	// NodeId length
	nodeIdLen := binary.LittleEndian.Uint16(headerBuf[offset:])

	// Read NodeId
	nodeIdBuf := make([]byte, nodeIdLen)
	if _, err := io.ReadFull(reader, nodeIdBuf); err != nil {
		return nil, fmt.Errorf("failed to read nodeId: %w", err)
	}
	header.NodeId = string(nodeIdBuf)

	// Read checksum placeholder (stored in header but actual verification happens at end of data)
	checksumBuf := make([]byte, 4)
	if _, err := io.ReadFull(reader, checksumBuf); err != nil {
		return nil, fmt.Errorf("failed to read header checksum: %w", err)
	}
	header.Checksum = binary.LittleEndian.Uint32(checksumBuf)

	return header, nil
}

func (s *store) close() error {
	if s == nil {
		return nil
	}

	s.closed.Store(true) // set pebbledb closed

	db := s.db.Load()
	if db != nil {
		db.Flush()
		db.Close()
		db = nil
	}

	return nil
}

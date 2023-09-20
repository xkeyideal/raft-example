package fsm

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/atomic"

	"github.com/cockroachdb/pebble"
	"github.com/hashicorp/raft"
	pebbledb "github.com/xkeyideal/raft-pebbledb"
)

type Store struct {
	baseDir string
	log     pebble.Logger
	db      *atomic.Pointer[pebble.DB]
	closed  *atomic.Bool
}

func NewStore(baseDir string) (*Store, error) {
	cfg := pebbledb.DefaultPebbleDBConfig()

	if err := os.MkdirAll(baseDir, os.ModePerm); err != nil {
		return nil, err
	}

	log := &Logger{}

	// 获取pebbledb的存储目录
	dbdir, err := getPebbleDBDir(baseDir)
	if err != nil {
		return nil, err
	}

	db, err := pebbledb.OpenPebbleDB(cfg, dbdir, log)
	if err != nil {
		return nil, err
	}

	return &Store{
		baseDir: baseDir,
		log:     log,
		db:      atomic.NewPointer[pebble.DB](db),
		closed:  atomic.NewBool(false),
	}, nil
}

func (s *Store) getColumnFamily(cf string) byte {
	return pebbleCfMap[cf]
}

func (s *Store) isclosed() bool {
	return s.closed.Load()
}

func (s *Store) getBytes(key []byte) ([]byte, error) {
	if s.closed.Load() {
		return []byte{}, pebble.ErrClosed
	}

	db := s.db.Load()
	val, closer, err := db.Get(key)

	// 查询的key不存在，返回空值
	if err == pebble.ErrNotFound {
		return []byte{}, nil
	}

	if err != nil {
		return nil, err
	}

	// 这里需要copy
	data := make([]byte, len(val))
	copy(data, val)

	if err := closer.Close(); err != nil {
		return nil, err
	}

	return data, nil
}

func (s *Store) buildColumnFamilyKey(cf byte, key []byte) []byte {
	return append([]byte{cf}, key...)
}

func (s *Store) batch() *pebble.Batch {
	db := s.db.Load()
	return db.NewBatch()
}

func (s *Store) write(b *pebble.Batch) error {
	return b.Commit(pebble.Sync)
}

func (s *Store) getIterator() *pebble.Iterator {
	db := s.db.Load()
	iter, _ := db.NewIter(&pebble.IterOptions{})
	return iter
}

func (s *Store) getSnapshot() *pebble.Snapshot {
	db := s.db.Load()
	return db.NewSnapshot()
}

func (s *Store) saveSnapShot(nodeId, raftAddr string, snapshot *pebble.Snapshot, sink raft.SnapshotSink) error {
	iter, _ := snapshot.NewIter(&pebble.IterOptions{})
	defer iter.Close()

	start := time.Now()

	// 将db里的数据全部遍历进内存
	var count uint64 = 0
	sz := make([]byte, 8)

	// 遍历pebblebd snapshot, 将数据写入 io.Writer
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		val := iter.Value()

		count++

		// 先写key
		binary.LittleEndian.PutUint64(sz, uint64(len(key)))
		if _, err := sink.Write(sz); err != nil { // key size
			sink.Cancel()
			return err
		}
		if _, err := sink.Write(key); err != nil { // key data
			sink.Cancel()
			return err
		}

		// 再写value
		// gzip encode
		gzipVal, err := gzipEncode(val)
		if err != nil {
			sink.Cancel()
			return err
		}

		binary.LittleEndian.PutUint64(sz, uint64(len(gzipVal)))
		if _, err := sink.Write(sz); err != nil { // val size
			sink.Cancel()
			return err
		}
		if _, err := sink.Write(gzipVal); err != nil { // val data
			sink.Cancel()
			return err
		}
	}

	s.log.Infof("SaveSnapshot %s-%s, start: %s, end: %s, cost: %d, count: %d\n",
		nodeId, raftAddr,
		start.String(), time.Now().String(), int64(time.Now().Sub(start))/1e6, count,
	)

	return sink.Close()
}

func (s *Store) recoverSnapShot(nodeId, raftAddr string, reader io.ReadCloser) error {
	// s.baseDir/uuid
	dbdir := getNewRandomDBDirName(s.baseDir)

	var oldDirName string

	name, err := getCurrentDBDirName(s.baseDir) // 从存储里拿到当前的current db目录
	if err != nil {
		return err
	}
	oldDirName = name

	newdb, err := pebbledb.OpenPebbleDB(pebbledb.DefaultPebbleDBConfig(), dbdir, s.log)
	if err != nil {
		return err
	}

	var count uint64 = 0
	sz := make([]byte, 8)
	start := time.Now()

	// 开始从snapshot reader里读取数据, 等到EOF退出
	for {
		count++

		// 先读key
		_, err := io.ReadFull(reader, sz) // key size
		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		toRead := binary.LittleEndian.Uint64(sz)
		kdata := make([]byte, toRead)
		_, err = io.ReadFull(reader, kdata) // key data
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// 再读val
		_, err = io.ReadFull(reader, sz) // val size
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		toRead = binary.LittleEndian.Uint64(sz)
		vdata := make([]byte, toRead)
		_, err = io.ReadFull(reader, vdata) // val data
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// gzip decode
		ungzipVData, err := gzipDecode(vdata)
		if err != nil {
			continue
		}

		// sync write db
		newdb.Set(kdata, ungzipVData, pebble.Sync)
	}

	newdb.Flush()

	if err := saveCurrentDBDirName(s.baseDir, dbdir); err != nil {
		return err
	}
	if err := replaceCurrentDBFile(s.baseDir); err != nil {
		return err
	}

	// 用新db置换老db，并close 老db
	old := s.db.Swap(newdb)
	if old != nil {
		old.Close()
	}

	// 删除旧的pebbledb存储的文件数据
	if err := os.RemoveAll(oldDirName); err != nil {
		return err
	}

	s.log.Infof("RecoverFromSnapshot %s-%s, start: %s, end: %s, cost: %d, count: %d, newDir: %s, oldDir: %s\n",
		nodeId, raftAddr,
		start.String(), time.Now().String(), int64(time.Now().Sub(start))/1e6, count,
		dbdir, oldDirName,
	)

	parent := filepath.Dir(oldDirName)
	return syncDir(parent)
}

func (s *Store) close() error {
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

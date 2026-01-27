package fsm

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	currentDBFilename  string = "pebble.running"
	updatingDBFilename string = "pebble.updating"

	cf_default = "default"
)

var (
	pebbleCfMap = map[string]byte{
		cf_default: 1,
	}
)

type Logger struct {
}

func (log *Logger) Infof(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

func (log *Logger) Errorf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

func (log *Logger) Fatalf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

// dirSize returns the total size of all files in the given directory
func DirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			// If the file doesn't exist, we can ignore it. Snapshot files might
			// disappear during walking.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return err
	})
	return size, err
}

func getPebbleDBDir(dir string) (string, error) {
	var dbdir string

	// check whether exist dir/pebble.running file
	newRunning := isNewRun(dir)

	// 全新启动的程序
	if newRunning { // if not exist pebble.running file
		// random generate a directory as pebbledb's storage directory
		dbdir = getNewRandomDBDirName(dir)
		if err := saveCurrentDBDirName(dir, dbdir); err != nil {
			return "", err
		}
		if err := replaceCurrentDBFile(dir); err != nil {
			return "", err
		}

		return dbdir, nil
	}

	if err := cleanupNodeDataDir(dir); err != nil {
		return "", err
	}

	var err error
	dbdir, err = getCurrentDBDirName(dir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dbdir); err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("db dir unexpectedly deleted")
		}
	}

	return dbdir, nil
}

// functions below are used to manage the current data directory of Pebble DB.
func isNewRun(dir string) bool {
	fp := filepath.Join(dir, currentDBFilename)
	if _, err := os.Stat(fp); os.IsNotExist(err) {
		return true
	}
	return false
}

func getNewRandomDBDirName(dir string) string {
	part := "%d_%d"
	rn := rand.Uint64()
	ct := time.Now().UnixNano()
	return filepath.Join(dir, fmt.Sprintf(part, rn, ct))
}

func replaceCurrentDBFile(dir string) error {
	fp := filepath.Join(dir, currentDBFilename)
	tmpFp := filepath.Join(dir, updatingDBFilename)
	if err := os.Rename(tmpFp, fp); err != nil {
		return err
	}
	return syncDir(dir)
}

func saveCurrentDBDirName(dir string, dbdir string) error {
	h := md5.New()
	if _, err := h.Write([]byte(dbdir)); err != nil {
		return err
	}
	fp := filepath.Join(dir, updatingDBFilename)
	f, err := os.Create(fp)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			panic(err)
		}
		if err := syncDir(dir); err != nil {
			panic(err)
		}
	}()
	if _, err := f.Write(h.Sum(nil)[:8]); err != nil {
		return err
	}
	if _, err := f.Write([]byte(dbdir)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return nil
}

func getCurrentDBDirName(dir string) (string, error) {
	fp := filepath.Join(dir, currentDBFilename)
	f, err := os.OpenFile(fp, os.O_RDONLY, 0755)
	if err != nil {
		return "", err
	}

	defer func() {
		f.Close()
	}()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if len(data) <= 8 {
		return "", errors.New("corrupted content")
	}
	crc := data[:8]
	content := data[8:]
	h := md5.New()
	if _, err := h.Write(content); err != nil {
		return "", err
	}
	if !bytes.Equal(crc, h.Sum(nil)[:8]) {
		return "", errors.New("corrupted content with not matched crc")
	}
	return string(content), nil
}

func createNodeDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dir))
}

func cleanupNodeDataDir(dir string) error {
	os.RemoveAll(filepath.Join(dir, updatingDBFilename))
	dbdir, err := getCurrentDBDirName(dir)
	if err != nil {
		return err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, fi := range files {
		if !fi.IsDir() {
			continue
		}

		toDelete := filepath.Join(dir, fi.Name())
		if toDelete != dbdir {
			if err := os.RemoveAll(toDelete); err != nil {
				return err
			}
		}
	}

	return nil
}

func syncDir(dir string) (err error) {
	if runtime.GOOS == "windows" {
		return nil
	}

	fileInfo, err := os.Stat(dir)
	if err != nil {
		return err
	}

	if !fileInfo.IsDir() {
		return nil
	}

	df, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return err
	}

	defer func() {
		if cerr := df.Close(); err == nil {
			err = cerr
		}
	}()

	return df.Sync()
}

func existFilePath(path string) bool {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsExist(err) {
			return true
		}

		return false
	}

	return true
}

func gzipEncode(content []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(content); err != nil {
		return nil, err
	}

	if err := writer.Flush(); err != nil {
		return nil, err
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	return buffer.Bytes(), nil
}

func gzipDecode(gzipMsg []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewBuffer(gzipMsg))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

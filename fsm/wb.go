package fsm

import "github.com/cockroachdb/pebble"

type pebbleWriteBatch struct {
	wb *pebble.Batch
	db *pebble.DB
	wo *pebble.WriteOptions
}

func newPebbleWriteBatch(db *pebble.DB) *pebbleWriteBatch {
	return &pebbleWriteBatch{
		wb: db.NewBatch(),
		db: db,
		wo: pebble.Sync,
	}
}

func (w *pebbleWriteBatch) Destroy() {
	w.wb.Commit(w.wo)
	w.wb.Close()
}

func (w *pebbleWriteBatch) Put(key []byte, val []byte) error {
	return w.wb.Set(key, val, w.wo)
}

func (w *pebbleWriteBatch) Delete(key []byte) error {
	return w.wb.Delete(key, w.wo)
}

func (w *pebbleWriteBatch) Commit() {
	w.wb.Commit(w.wo)
	w.wb.Close()

	w.wb = w.db.NewBatch()
}

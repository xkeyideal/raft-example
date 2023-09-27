package fsm

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble"
)

type CommandType int

const (
	Query  CommandType = 0
	Insert CommandType = 1
)

type Command struct {
	Type    CommandType `json:"type"`
	Payload []byte      `json:"payload"`
}

type SetPayload struct {
	Key   []byte `json:"key"`
	Value []byte `json:"val"`
}

type CommandResponse struct {
	Val   []byte `json:"val"`
	Error error  `json:"error"`
}

func (store *store) applyCommand(data []byte) *CommandResponse {
	c := &Command{}
	err := json.Unmarshal(data, c)
	if err != nil {
		return &CommandResponse{
			Error: err,
		}
	}

	switch c.Type {
	case Query:
		val, err := store.query(c.Payload)
		return &CommandResponse{
			Val:   val,
			Error: err,
		}
	case Insert:
		err := store.insert(c.Payload)
		return &CommandResponse{
			Error: err,
		}
	default:
		return &CommandResponse{
			Error: errors.New("command is undefined"),
		}
	}
}

func (store *store) insert(data []byte) error {
	var sp SetPayload
	err := json.Unmarshal(data, &sp)
	if err != nil {
		return fmt.Errorf("Could not parse payload: %s", err)
	}

	batch := store.batch()
	defer batch.Close()

	cf := store.getColumnFamily(cf_default)
	batch.Set(store.buildColumnFamilyKey(cf, sp.Key), sp.Value, pebble.Sync)
	return store.write(batch)
}

func (store *store) query(key []byte) ([]byte, error) {
	if store.isclosed() {
		return nil, pebble.ErrClosed
	}

	cf := store.getColumnFamily(cf_default)
	d, err := store.getBytes(store.buildColumnFamilyKey(cf, key))
	if err != nil {
		return nil, err
	}

	return d, nil
}

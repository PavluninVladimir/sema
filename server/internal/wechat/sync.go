package wechat

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

type syncCommand struct {
	id      int32
	payload []byte
}
type syncBatch struct {
	code         int32
	cursor       []byte
	commands     []syncCommand
	continuation bool
}

func readUint32(fields []wireField, number uint32) (uint32, error) {
	value, err := scalarField(fields, number)
	if err != nil {
		return 0, err
	}
	if value > uint64(^uint32(0)) {
		return 0, errors.New("числовое поле WeChat переполнено")
	}
	return uint32(value), nil
}

func makeSyncRequest(cursor []byte, device string, scene uint32) ([]byte, error) {
	if len(cursor) == 0 {
		cursor = []byte{0}
	}
	if len(cursor) > 65536 || len(device) == 0 || len(device) > 1024 || !utf8.ValidString(device) || strings.ContainsRune(device, 0) {
		return nil, errors.New("неверные параметры синхронизации WeChat")
	}
	b, _ := appendBytes(nil, 1, []byte{8, 0})
	b, _ = appendUint(b, 2, 7)
	buf, _ := packBuffer(cursor)
	b, _ = appendBytes(b, 3, buf)
	b, _ = appendUint(b, 4, uint64(scene))
	b, _ = appendBytes(b, 5, []byte(device))
	return appendUint(b, 9, 0)
}

func readCursor(input []byte) (map[uint32]uint32, error) {
	out := map[uint32]uint32{}
	if len(input) == 0 {
		return out, nil
	}
	if len(input) > 65536 {
		return nil, errors.New("слишком большой курсор WeChat")
	}
	f, err := decodeFields(input)
	if err != nil {
		return nil, err
	}
	if _, err = readUint32(f, 1); err != nil {
		return nil, err
	}
	for _, item := range f {
		if item.number != 2 {
			continue
		}
		if item.kind != 2 {
			return nil, errors.New("неверный элемент курсора WeChat")
		}
		entry, err := decodeFields(item.data)
		if err != nil {
			return nil, err
		}
		key, err := readUint32(entry, 1)
		if err != nil {
			return nil, err
		}
		value, err := readUint32(entry, 2)
		if err != nil {
			return nil, err
		}
		out[key] = max(out[key], value)
	}
	return out, nil
}

func mergeCursor(old, next []byte, reset bool) ([]byte, error) {
	incoming, err := readCursor(next)
	if err != nil {
		return nil, err
	}
	if reset || len(old) == 0 {
		return bytes.Clone(next), nil
	}
	if len(next) == 0 {
		return bytes.Clone(old), nil
	}
	existing, err := readCursor(old)
	if err != nil {
		return nil, err
	}
	for k, v := range incoming {
		existing[k] = max(existing[k], v)
	}
	keys := make([]uint32, 0, len(existing))
	for k := range existing {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	result, _ := appendUint(nil, 1, uint64(len(keys)))
	for _, k := range keys {
		item, _ := appendUint(nil, 1, uint64(k))
		item, _ = appendUint(item, 2, uint64(existing[k]))
		result, err = appendBytes(result, 2, item)
		if err != nil {
			return nil, err
		}
		if len(result) > 65536 {
			return nil, errors.New("слишком большой объединённый курсор WeChat")
		}
	}
	return result, nil
}

func decodeSync(input, prior []byte) (syncBatch, error) {
	var batch syncBatch
	f, err := decodeFields(input)
	if err != nil {
		return batch, err
	}
	rawCode, err := scalarField(f, 1)
	if err != nil {
		return batch, err
	}
	code := int32(uint32(rawCode))
	if code != 0 && code != -2006 {
		return batch, &serviceError{operation: "синхронизация", code: code}
	}
	flags, err := readUint32(f, 3)
	if err != nil {
		return batch, err
	}
	raw, err := dataField(f, 4)
	if err != nil {
		return batch, err
	}
	incoming, err := unpackBuffer(raw)
	if err != nil {
		return batch, err
	}
	cursor, err := mergeCursor(prior, incoming, code == -2006)
	if err != nil {
		return batch, err
	}
	raw, err = dataField(f, 2)
	if err != nil {
		return batch, err
	}
	list, err := decodeFields(raw)
	if err != nil {
		return batch, err
	}
	count, err := readUint32(list, 1)
	if err != nil {
		return batch, err
	}
	var commands []syncCommand
	for _, item := range list {
		if item.number != 2 {
			continue
		}
		if item.kind != 2 {
			return batch, errors.New("неверная команда синхронизации WeChat")
		}
		entry, err := decodeFields(item.data)
		if err != nil {
			return batch, err
		}
		id, err := scalarField(entry, 1)
		if err != nil {
			return batch, err
		}
		wrapped, err := dataField(entry, 2)
		if err != nil {
			return batch, err
		}
		payload, err := unpackBuffer(wrapped)
		if err != nil {
			return batch, err
		}
		commands = append(commands, syncCommand{id: int32(uint32(id)), payload: payload})
	}
	if uint32(len(commands)) != count {
		return batch, errors.New("число команд WeChat не совпало")
	}
	sent := prior
	if len(prior) == 0 {
		sent = []byte{0}
	}
	return syncBatch{code: code, cursor: cursor, commands: commands, continuation: flags&7 != 0 && !bytes.Equal(incoming, sent)}, nil
}

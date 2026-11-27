package wechat

import (
	"encoding/binary"
	"errors"
)

type longlinkPacket struct {
	command, sequence uint32
	payload           []byte
}

func packLonglink(command, sequence uint32, payload []byte) ([]byte, error) {
	if len(payload) > 65503 {
		return nil, errors.New("пакет longlink слишком велик")
	}
	b := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
	binary.BigEndian.PutUint16(b[4:6], 16)
	binary.BigEndian.PutUint16(b[6:8], 1)
	binary.BigEndian.PutUint32(b[8:12], command)
	binary.BigEndian.PutUint32(b[12:16], sequence)
	copy(b[16:], payload)
	return b, nil
}

func unpackLonglink(input []byte) (longlinkPacket, error) {
	var p longlinkPacket
	if len(input) < 16 || len(input) > maxMessageBytes+16 || uint64(binary.BigEndian.Uint32(input[:4])) != uint64(len(input)) || binary.BigEndian.Uint16(input[4:6]) != 16 || binary.BigEndian.Uint16(input[6:8]) != 1 {
		return p, errors.New("неверный пакет longlink")
	}
	p.command = binary.BigEndian.Uint32(input[8:12])
	p.sequence = binary.BigEndian.Uint32(input[12:16])
	p.payload = append([]byte(nil), input[16:]...)
	return p, nil
}

type longlinkDecoder struct {
	pending    []byte
	maxPayload int
}

func (d *longlinkDecoder) feed(data []byte) error {
	if d.maxPayload <= 0 || d.maxPayload > maxMessageBytes {
		return errors.New("неверный предел longlink")
	}
	if len(data) > d.maxPayload+65540-len(d.pending) {
		return errors.New("буфер longlink переполнен")
	}
	d.pending = append(d.pending, data...)
	return nil
}
func (d *longlinkDecoder) next() (*longlinkPacket, error) {
	if d.maxPayload <= 0 || d.maxPayload > maxMessageBytes {
		return nil, errors.New("неверный предел longlink")
	}
	if len(d.pending) < 4 {
		return nil, nil
	}
	n := uint64(binary.BigEndian.Uint32(d.pending[:4]))
	if n < 16 || n > uint64(d.maxPayload+16) {
		return nil, errors.New("неверная длина longlink")
	}
	if len(d.pending) >= 8 && (binary.BigEndian.Uint16(d.pending[4:6]) != 16 || binary.BigEndian.Uint16(d.pending[6:8]) != 1) {
		return nil, errors.New("неверный заголовок longlink")
	}
	if len(d.pending) < int(n) {
		return nil, nil
	}
	p, err := unpackLonglink(d.pending[:int(n)])
	if err != nil {
		return nil, err
	}
	d.pending = d.pending[int(n):]
	return &p, nil
}

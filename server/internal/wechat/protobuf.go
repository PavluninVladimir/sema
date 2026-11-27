// Package wechat implements the private-account WeChat protocol for Sema.
// The wire codec is adapted from agent-wechat, revision c67b9389da19619f02b812b95fb956dfb0424c4c.
// Copyright (c) 2026 Nick Yu and contributors; see LICENSE.agent-wechat.
package wechat

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const maxMessageBytes = 16 * 1024 * 1024
const maxFields = 65536

// wireField retains unknown fields and their original wire type.
type wireField struct {
	number uint32
	kind   byte
	scalar uint64
	data   []byte
}

func appendUint(dst []byte, number uint32, value uint64) ([]byte, error) {
	if number == 0 || number >= 1<<29 {
		return nil, errors.New("недопустимый номер поля")
	}
	dst = binary.AppendUvarint(dst, uint64(number)<<3)
	return binary.AppendUvarint(dst, value), nil
}

func appendBytes(dst []byte, number uint32, value []byte) ([]byte, error) {
	if number == 0 || number >= 1<<29 {
		return nil, errors.New("недопустимый номер поля")
	}
	if len(value) > maxMessageBytes || len(dst) > maxMessageBytes-len(value) {
		return nil, errors.New("сообщение слишком велико")
	}
	dst = binary.AppendUvarint(dst, uint64(number)<<3|2)
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	if len(dst) > maxMessageBytes-len(value) {
		return nil, errors.New("сообщение слишком велико")
	}
	return append(dst, value...), nil
}

func decodeFields(input []byte) ([]wireField, error) {
	if len(input) > maxMessageBytes {
		return nil, errors.New("сообщение слишком велико")
	}
	fields := make([]wireField, 0)
	for len(input) > 0 {
		if len(fields) >= maxFields {
			return nil, errors.New("слишком много полей")
		}
		key, n := binary.Uvarint(input)
		if n <= 0 {
			return nil, errors.New("повреждённый тег")
		}
		input = input[n:]
		number := key >> 3
		if number == 0 || number >= 1<<29 {
			return nil, errors.New("недопустимый номер поля")
		}
		f := wireField{number: uint32(number), kind: byte(key & 7)}
		switch f.kind {
		case 0:
			v, n := binary.Uvarint(input)
			if n <= 0 {
				return nil, errors.New("повреждённое число")
			}
			f.scalar = v
			input = input[n:]
		case 1, 2, 5:
			var length uint64
			switch f.kind {
			case 1:
				length = 8
			case 5:
				length = 4
			case 2:
				v, n := binary.Uvarint(input)
				if n <= 0 {
					return nil, errors.New("повреждённая длина")
				}
				length = v
				input = input[n:]
			}
			if length > maxMessageBytes || length > uint64(len(input)) {
				return nil, errors.New("обрезанное поле")
			}
			f.data = append([]byte(nil), input[:int(length)]...)
			input = input[int(length):]
		default:
			return nil, errors.New("неподдерживаемый тип поля")
		}
		fields = append(fields, f)
	}
	return fields, nil
}

func optionalField(fields []wireField, number uint32) (*wireField, error) {
	var found *wireField
	for i := range fields {
		if fields[i].number == number {
			if found != nil {
				return nil, fmt.Errorf("повтор поля %d", number)
			}
			found = &fields[i]
		}
	}
	return found, nil
}

func scalarField(fields []wireField, number uint32) (uint64, error) {
	f, err := optionalField(fields, number)
	if err != nil {
		return 0, err
	}
	if f == nil || f.kind != 0 {
		return 0, fmt.Errorf("отсутствует числовое поле %d", number)
	}
	return f.scalar, nil
}

func dataField(fields []wireField, number uint32) ([]byte, error) {
	f, err := optionalField(fields, number)
	if err != nil {
		return nil, err
	}
	if f == nil || f.kind != 2 {
		return nil, fmt.Errorf("отсутствует поле данных %d", number)
	}
	return f.data, nil
}

func packBuffer(data []byte) ([]byte, error) {
	b, err := appendUint(nil, 1, uint64(len(data)))
	if err != nil {
		return nil, err
	}
	return appendBytes(b, 2, data)
}

func unpackBuffer(input []byte) ([]byte, error) {
	fields, err := decodeFields(input)
	if err != nil {
		return nil, err
	}
	size, err := scalarField(fields, 1)
	if err != nil {
		return nil, err
	}
	field, err := optionalField(fields, 2)
	if err != nil {
		return nil, err
	}
	var data []byte
	if field != nil {
		if field.kind != 2 {
			return nil, errors.New("неверный тип буфера")
		}
		data = field.data
	}
	if size != uint64(len(data)) {
		return nil, errors.New("длина буфера не совпадает")
	}
	return data, nil
}

func baseCode(fields []wireField) (int32, error) {
	raw, err := dataField(fields, 1)
	if err != nil {
		return 0, err
	}
	base, err := decodeFields(raw)
	if err != nil {
		return 0, err
	}
	code, err := scalarField(base, 1)
	if err != nil {
		return 0, err
	}
	return int32(uint32(code)), nil
}

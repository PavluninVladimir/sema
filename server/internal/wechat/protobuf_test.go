package wechat

import (
	"bytes"
	"math"
	"testing"
)

func TestWireFields(t *testing.T) {
	var b []byte
	b, _ = appendUint(b, 1, math.MaxUint64)
	b, _ = appendBytes(b, 900, []byte("unknown"))
	b, _ = appendUint(b, 1, 7)
	fields, err := decodeFields(b)
	if err != nil || len(fields) != 3 || fields[0].scalar != math.MaxUint64 || !bytes.Equal(fields[1].data, []byte("unknown")) {
		t.Fatalf("decode: %#v, %v", fields, err)
	}
	if _, err = optionalField(fields, 1); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestMalformedWireFields(t *testing.T) {
	tests := [][]byte{{0}, {8, 255}, {10, 3, 0}, {8, 255, 255, 255, 255, 255, 255, 255, 255, 255, 2}, {11}, {9, 1}, {13, 1}, {10, 255, 255, 255, 255, 255, 255, 255, 255, 255, 1}}
	for _, input := range tests {
		if _, err := decodeFields(input); err == nil {
			t.Errorf("accepted %x", input)
		}
	}
	if _, err := decodeFields(make([]byte, maxMessageBytes+1)); err == nil {
		t.Fatal("oversized input accepted")
	}
	if _, err := appendBytes(nil, 0, nil); err == nil {
		t.Fatal("invalid tag accepted")
	}
}

func TestFixedFieldsAndCopy(t *testing.T) {
	input := []byte{9, 1, 2, 3, 4, 5, 6, 7, 8, 21, 9, 10, 11, 12}
	f, err := decodeFields(input)
	if err != nil || len(f) != 2 || f[0].kind != 1 || f[1].kind != 5 {
		t.Fatalf("decode: %#v %v", f, err)
	}
	input[1] = 99
	if f[0].data[0] != 1 {
		t.Fatal("field aliases input")
	}
}

func TestBufferAndBaseCode(t *testing.T) {
	for _, original := range [][]byte{nil, []byte("тест"), bytes.Repeat([]byte{3}, 4096)} {
		packed, err := packBuffer(original)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := unpackBuffer(packed)
		if err != nil || !bytes.Equal(decoded, original) {
			t.Fatalf("buffer: %x %v", decoded, err)
		}
	}
	if _, err := unpackBuffer([]byte{8, 2, 18, 1, 'a'}); err == nil {
		t.Fatal("mismatched size accepted")
	}
	base, _ := appendUint(nil, 1, uint64(math.MaxUint64))
	msg, _ := appendBytes(nil, 1, base)
	fields, err := decodeFields(msg)
	if err != nil {
		t.Fatal(err)
	}
	code, err := baseCode(fields)
	if err != nil || code != -1 {
		t.Fatalf("code %d: %v", code, err)
	}
}

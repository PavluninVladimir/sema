package wechat

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const mmtlsVersion uint16 = 0xf104
const mmtlsMaxPlain = 65535 - 16

type mmtlsRecord struct {
	header [5]byte
	body   []byte
}

func mmtlsFrame(kind byte, body []byte) ([]byte, error) {
	if kind < 0x15 || kind > 0x19 || len(body) > 65535 {
		return nil, errors.New("неверная запись MMTLS")
	}
	result := make([]byte, 5+len(body))
	result[0] = kind
	binary.BigEndian.PutUint16(result[1:3], mmtlsVersion)
	binary.BigEndian.PutUint16(result[3:5], uint16(len(body)))
	copy(result[5:], body)
	return result, nil
}

func parseMMTLSRecord(input []byte) (mmtlsRecord, error) {
	var r mmtlsRecord
	if len(input) < 5 {
		return r, errors.New("обрезанный заголовок MMTLS")
	}
	if input[0] < 0x15 || input[0] > 0x19 || binary.BigEndian.Uint16(input[1:3]) != mmtlsVersion || int(binary.BigEndian.Uint16(input[3:5])) != len(input)-5 {
		return r, errors.New("неверный заголовок MMTLS")
	}
	copy(r.header[:], input[:5])
	r.body = append([]byte(nil), input[5:]...)
	return r, nil
}

func mmtlsNonce(iv []byte, sequence uint64) ([]byte, error) {
	if len(iv) != 12 {
		return nil, errors.New("неверный IV MMTLS")
	}
	nonce := append([]byte(nil), iv...)
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], sequence)
	for i := range seq {
		nonce[i+4] ^= seq[i]
	}
	return nonce, nil
}

func sealMMTLS(kind byte, plain, key, iv []byte, sequence uint64) ([]byte, error) {
	if len(plain) > mmtlsMaxPlain {
		return nil, errors.New("запись MMTLS слишком велика")
	}
	frame, err := mmtlsFrame(kind, make([]byte, len(plain)+16))
	if err != nil {
		return nil, err
	}
	nonce, err := mmtlsNonce(iv, sequence)
	if err != nil {
		return nil, err
	}
	var aad [13]byte
	binary.BigEndian.PutUint64(aad[:8], sequence)
	copy(aad[8:], frame[:5])
	body, err := gcmSeal(key, nonce, aad[:], plain)
	if err != nil {
		return nil, err
	}
	copy(frame[5:], body)
	return frame, nil
}

func openMMTLS(r mmtlsRecord, key, iv []byte, sequence uint64) ([]byte, error) {
	if len(r.body) < 16 || int(binary.BigEndian.Uint16(r.header[3:5])) != len(r.body) || r.header[0] < 0x15 || r.header[0] > 0x19 || binary.BigEndian.Uint16(r.header[1:3]) != mmtlsVersion {
		return nil, errors.New("неверная запись MMTLS")
	}
	nonce, err := mmtlsNonce(iv, sequence)
	if err != nil {
		return nil, err
	}
	var aad [13]byte
	binary.BigEndian.PutUint64(aad[:8], sequence)
	copy(aad[8:], r.header[:])
	return gcmOpen(key, nonce, aad[:], r.body)
}

type mmtlsTraffic struct{ clientKey, serverKey, clientIV, serverIV []byte }

func deriveMMTLSTraffic(secret []byte, label string, transcript []byte) (mmtlsTraffic, error) {
	var t mmtlsTraffic
	sum := sha256.Sum256(transcript)
	info := append([]byte(label), sum[:]...)
	b, err := hkdfExpand(secret, info, 56)
	if err != nil {
		return t, err
	}
	defer clear(b)
	t.clientKey = append([]byte(nil), b[:16]...)
	t.serverKey = append([]byte(nil), b[16:32]...)
	t.clientIV = append([]byte(nil), b[32:44]...)
	t.serverIV = append([]byte(nil), b[44:]...)
	return t, nil
}

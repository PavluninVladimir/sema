package wechat

import (
	"crypto/md5"
	"encoding/binary"
	"errors"
	"hash/adler32"
)

type packetPolicy struct {
	clientVersion, uin                  uint32
	cookie                              []byte
	cgi                                 uint16
	originalLength, preEncryptionLength int
	keyVersion, certVersion             uint16
	checksum                            uint32
	compression, encryption             byte
	noticeID                            uint32
	sequence                            uint16
}

func packBusiness(p packetPolicy, encrypted []byte) ([]byte, error) {
	if len(p.cookie) > 15 || p.compression > 3 || p.encryption > 15 ||
		p.originalLength < 0 || p.originalLength > maxMessageBytes ||
		p.preEncryptionLength < 0 || p.preEncryptionLength > maxMessageBytes ||
		len(encrypted) > maxMessageBytes+65536 {
		return nil, errors.New("недопустимые параметры пакета WeChat")
	}
	b := []byte{0xbf, 0, 0}
	b = binary.BigEndian.AppendUint32(b, p.clientVersion)
	b = binary.BigEndian.AppendUint32(b, p.uin)
	b = append(b, p.cookie...)
	for _, v := range []uint64{uint64(p.cgi), uint64(p.originalLength), uint64(p.preEncryptionLength), uint64(p.keyVersion), uint64(p.certVersion), uint64(p.checksum)} {
		b = binary.AppendUvarint(b, v)
	}
	b = append(b, 0xff)
	b = binary.AppendUvarint(b, uint64(p.noticeID))
	b = append(b, 0, 0, 0)
	b = binary.BigEndian.AppendUint16(b, p.sequence)
	if len(b) > 63 {
		return nil, errors.New("слишком длинный заголовок пакета WeChat")
	}
	flags := uint16(len(b))<<2 | uint16(p.compression) | uint16(len(p.cookie))<<8 | uint16(p.encryption)<<12
	binary.LittleEndian.PutUint16(b[1:3], flags)
	return append(b, encrypted...), nil
}

type businessEnvelope struct {
	code                                int32
	cgi                                 uint64
	originalLength, preEncryptionLength int
	compression, encryption             byte
	cookie, payload                     []byte
}

func splitBusiness(input []byte) (businessEnvelope, error) {
	var result businessEnvelope
	if len(input) < 10 || len(input) > maxMessageBytes+65536 {
		return result, errors.New("неверная длина ответа WeChat")
	}
	base := 0
	if input[0] == 0xbf {
		base = 1
	}
	flags := binary.LittleEndian.Uint16(input[base : base+2])
	end := int(flags >> 2 & 63)
	start := base + 10
	cookieEnd := start + int(flags>>8&15)
	if end < cookieEnd || end > len(input) {
		return result, errors.New("обрезанный заголовок ответа WeChat")
	}
	header := input[cookieEnd:end]
	var fields [5]uint64
	for i := range fields {
		v, n := binary.Uvarint(header)
		if n <= 0 {
			return result, errors.New("повреждённое поле заголовка ответа WeChat")
		}
		fields[i] = v
		header = header[n:]
	}
	if (base == 0 && len(header) != 0) || fields[1] > maxMessageBytes || fields[2] > maxMessageBytes {
		return result, errors.New("недопустимые параметры ответа WeChat")
	}
	return businessEnvelope{
		code: int32(binary.BigEndian.Uint32(input[base+2 : base+6])),
		cgi:  fields[0], originalLength: int(fields[1]), preEncryptionLength: int(fields[2]),
		compression: byte(flags & 3), encryption: byte(flags >> 12),
		cookie: input[start:cookieEnd], payload: input[end:],
	}, nil
}

// MD5 is required by WeChat's checksum format here. MMTLS authenticates the transfer.
func businessChecksum(uin uint32, payload []byte) uint32 {
	id := md5.Sum(binary.BigEndian.AppendUint32(nil, uin))
	prefix := md5.Sum(append(binary.BigEndian.AppendUint32(nil, uint32(len(payload))), id[:]...))
	checksum := adler32.New()
	checksum.Write(prefix[:])
	checksum.Write(payload)
	return checksum.Sum32()
}

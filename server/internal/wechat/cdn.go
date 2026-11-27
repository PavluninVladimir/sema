package wechat

import (
	"crypto/aes"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"maps"
	"net/netip"
	"slices"
)

const maxCDNBody = 2 * 1024 * 1024
const maxCDNFile = 25 * 1024 * 1024

type cdnFields map[string][]byte

func validCDNField(key string) bool {
	if len(key) == 0 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func packCDN(fields cdnFields) ([]byte, error) {
	if len(fields) > 2048 {
		return nil, errors.New("слишком много полей CDN WeChat")
	}
	var out []byte
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		value := fields[key]
		if !validCDNField(key) || len(value) > maxCDNBody-len(out)-len(key)-8 {
			return nil, errors.New("неверное поле запроса CDN WeChat")
		}
		out = binary.BigEndian.AppendUint32(out, uint32(len(key)))
		out = append(out, key...)
		out = binary.BigEndian.AppendUint32(out, uint32(len(value)))
		out = append(out, value...)
	}
	return out, nil
}

func unpackCDN(input []byte) (cdnFields, error) {
	if len(input) > maxCDNBody {
		return nil, errors.New("слишком большой ответ CDN WeChat")
	}
	c := handshakeCursor{input}
	fields := make(cdnFields)
	for len(c.b) > 0 {
		if len(fields) >= 256 {
			return nil, errors.New("слишком много полей CDN WeChat")
		}
		name, err := c.sized(4)
		if err != nil {
			return nil, err
		}
		key := string(name)
		if !validCDNField(key) {
			return nil, errors.New("неверное имя поля CDN WeChat")
		}
		if _, exists := fields[key]; exists {
			return nil, errors.New("дублированное поле CDN WeChat")
		}
		value, err := c.sized(4)
		if err != nil {
			return nil, err
		}
		fields[key] = value
	}
	return fields, nil
}

func publicCDNAddress(ip netip.Addr) bool {
	if !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	a := ip.As4()
	switch a[0] {
	case 0, 127:
		return false
	case 100:
		return a[1] < 64 || a[1] > 127
	case 192:
		return a[1] != 0 && (a[1] != 2 || a[2] != 0)
	case 198:
		return a[1] != 18 && a[1] != 19 && (a[1] != 51 || a[2] != 100)
	case 203:
		return a[1] != 0 || a[2] != 113
	}
	return a[0] < 224
}

type cdnConfig struct {
	version uint32
	uin     uint32
	authKey []byte
	servers []netip.Addr
}

func decodeCDNConfig(input []byte, uin uint32) (cdnConfig, error) {
	var conf cdnConfig
	if uin == 0 {
		return conf, errors.New("нужен идентификатор аккаунта WeChat")
	}
	f, err := loginResponseFields(input, "настройка CDN")
	if err != nil {
		return conf, err
	}
	raw, err := dataField(f, 2)
	if err != nil {
		return conf, err
	}
	group, err := decodeFields(raw)
	if err != nil {
		return conf, err
	}
	version, err := readUint32(group, 1)
	if err != nil {
		return conf, err
	}
	owner, err := readUint32(group, 2)
	if err != nil || owner != uin {
		return conf, errors.New("CDN WeChat ответил для другого аккаунта")
	}
	raw, err = dataField(group, 8)
	if err != nil {
		return conf, err
	}
	key, err := unpackBuffer(raw)
	if err != nil {
		return conf, err
	}
	if len(key) == 0 || len(key) > 65536 {
		return conf, errors.New("неверный ключ CDN WeChat")
	}
	var servers []netip.Addr
	for _, field := range group {
		if field.number != 6 {
			continue
		}
		if field.kind != 2 || len(servers) >= 32 {
			return conf, errors.New("неверный список серверов CDN WeChat")
		}
		parts, err := decodeFields(field.data)
		if err != nil {
			return conf, err
		}
		addr, err := dataField(parts, 1)
		if err != nil {
			return conf, err
		}
		ip, err := netip.ParseAddr(string(addr))
		if err != nil || !publicCDNAddress(ip) {
			return conf, errors.New("недопустимый адрес CDN WeChat")
		}
		servers = append(servers, ip)
	}
	if len(servers) == 0 {
		return conf, errors.New("WeChat не выдал адрес CDN")
	}
	return cdnConfig{version: version, uin: uin, authKey: key, servers: servers}, nil
}

// The CDN uses AES-ECB with PKCS#7 padding. Integrity and size must also be
// verified against the authenticated file descriptor before publishing a file.
func cdnCrypt(input, key []byte, decrypt bool) ([]byte, error) {
	if len(key) != 16 || len(input) > maxCDNFile+16 {
		return nil, errors.New("неверные параметры шифрования CDN WeChat")
	}
	cipher, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if decrypt {
		if len(input) == 0 || len(input)%16 != 0 {
			return nil, errors.New("обрезанный файл CDN WeChat")
		}
		out := make([]byte, len(input))
		for i := 0; i < len(input); i += 16 {
			cipher.Decrypt(out[i:i+16], input[i:i+16])
		}
		pad := out[len(out)-1]
		ok := subtle.ConstantTimeLessOrEq(1, int(pad)) & subtle.ConstantTimeLessOrEq(int(pad), 16)
		for i := 1; i <= 16; i++ {
			active := subtle.ConstantTimeLessOrEq(i, int(pad))
			ok &= subtle.ConstantTimeSelect(active, subtle.ConstantTimeByteEq(out[len(out)-i], pad), 1)
		}
		if ok != 1 {
			clear(out)
			return nil, errors.New("файл CDN WeChat повреждён")
		}
		return out[:len(out)-int(pad)], nil
	}
	if len(input) > maxCDNFile {
		return nil, errors.New("файл CDN WeChat слишком велик")
	}
	pad := 16 - len(input)%16
	out := make([]byte, len(input)+pad)
	copy(out, input)
	for i := len(input); i < len(out); i++ {
		out[i] = byte(pad)
	}
	for i := 0; i < len(out); i += 16 {
		cipher.Encrypt(out[i:i+16], out[i:i+16])
	}
	return out, nil
}

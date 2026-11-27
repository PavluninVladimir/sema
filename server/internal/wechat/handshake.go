package wechat

import (
	"encoding/binary"
	"errors"
)

type handshakeCursor struct{ b []byte }

func (c *handshakeCursor) take(n int) ([]byte, error) {
	if n < 0 || n > len(c.b) {
		return nil, errors.New("обрезанная структура MMTLS")
	}
	v := c.b[:n]
	c.b = c.b[n:]
	return v, nil
}
func (c *handshakeCursor) uint(n int) (uint64, error) {
	if n < 1 || n > 8 {
		return 0, errors.New("неверная длина числа")
	}
	b, e := c.take(n)
	if e != nil {
		return 0, e
	}
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v, nil
}
func (c *handshakeCursor) sized(width int) ([]byte, error) {
	n, e := c.uint(width)
	if e != nil {
		return nil, e
	}
	if n > uint64(len(c.b)) {
		return nil, errors.New("обрезанная секция")
	}
	return c.take(int(n))
}
func (c *handshakeCursor) end() error {
	if len(c.b) != 0 {
		return errors.New("лишние данные структуры")
	}
	return nil
}

func handshakeSection(body []byte) ([]byte, error) {
	if uint64(len(body)) > uint64(^uint32(0)) {
		return nil, errors.New("секция слишком велика")
	}
	v := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(v, uint32(len(body)))
	copy(v[4:], body)
	return v, nil
}
func handshakeSized(body []byte) ([]byte, error) {
	if len(body) > 65535 {
		return nil, errors.New("секция слишком велика")
	}
	v := make([]byte, 2+len(body))
	binary.BigEndian.PutUint16(v, uint16(len(body)))
	copy(v[2:], body)
	return v, nil
}

type serverHello struct {
	keyID, region, version uint32
	point                  []byte
}

func parseServerHello(input []byte) (serverHello, error) {
	var hello serverHello
	top := handshakeCursor{input}
	body, e := top.sized(4)
	if e != nil {
		return hello, e
	}
	if e = top.end(); e != nil {
		return hello, e
	}
	c := handshakeCursor{body}
	typ, e := c.uint(1)
	if e != nil {
		return hello, e
	}
	ver, e := c.take(2)
	if e != nil {
		return hello, e
	}
	suite, e := c.uint(2)
	if e != nil {
		return hello, e
	}
	if typ != 2 || binary.LittleEndian.Uint16(ver) != mmtlsVersion || suite != 0xc02b {
		return hello, errors.New("неподдерживаемое ServerHello")
	}
	if _, e = c.take(32); e != nil {
		return hello, e
	}
	ext, e := c.sized(4)
	if e != nil {
		return hello, e
	}
	if e = c.end(); e != nil {
		return hello, e
	}
	x := handshakeCursor{ext}
	count, e := x.uint(1)
	if e != nil {
		return hello, e
	}
	seen := map[uint64]bool{}
	hasShare, hasCert := false, false
	for i := uint64(0); i < count; i++ {
		raw, e := x.sized(4)
		if e != nil {
			return hello, e
		}
		item := handshakeCursor{raw}
		kind, e := item.uint(2)
		if e != nil {
			return hello, e
		}
		if seen[kind] {
			return hello, errors.New("повтор расширения MMTLS")
		}
		seen[kind] = true
		switch kind {
		case 0x11:
			id, e := item.uint(4)
			if e != nil {
				return hello, e
			}
			point, e := item.sized(2)
			if e != nil {
				return hello, e
			}
			if len(point) != 65 || point[0] != 4 {
				return hello, errors.New("неверная точка ECDH")
			}
			if e = item.end(); e != nil {
				return hello, e
			}
			hello.keyID = uint32(id)
			hello.point = append([]byte(nil), point...)
			hasShare = true
		case 0x13:
			region, e := item.uint(4)
			if e != nil {
				return hello, e
			}
			version, e := item.uint(4)
			if e != nil {
				return hello, e
			}
			if e = item.end(); e != nil {
				return hello, e
			}
			hello.region = uint32(region)
			hello.version = uint32(version)
			hasCert = true
		}
	}
	if e = x.end(); e != nil {
		return hello, e
	}
	if !hasShare || !hasCert {
		return hello, errors.New("отсутствует расширение MMTLS")
	}
	return hello, nil
}

type mmtlsTicket struct {
	kind               byte
	lifetime, reserved uint32
	nonce, opaque      []byte
}

func parseMMTLSTickets(input []byte) ([]mmtlsTicket, error) {
	top := handshakeCursor{input}
	body, e := top.sized(4)
	if e != nil {
		return nil, e
	}
	if e = top.end(); e != nil {
		return nil, e
	}
	c := handshakeCursor{body}
	kind, e := c.uint(1)
	if e != nil {
		return nil, e
	}
	count, e := c.uint(1)
	if e != nil {
		return nil, e
	}
	if kind != 4 || count < 1 || count > 4 {
		return nil, errors.New("неверное число билетов")
	}
	out := make([]mmtlsTicket, 0, count)
	for i := uint64(0); i < count; i++ {
		raw, e := c.sized(4)
		if e != nil {
			return nil, e
		}
		t := handshakeCursor{raw}
		kind, e := t.uint(1)
		if e != nil {
			return nil, e
		}
		life, e := t.uint(4)
		if e != nil {
			return nil, e
		}
		if _, e = t.sized(2); e != nil {
			return nil, e
		}
		reserved, e := t.uint(4)
		if e != nil {
			return nil, e
		}
		nonce, e := t.sized(2)
		if e != nil {
			return nil, e
		}
		opaque, e := t.sized(2)
		if e != nil {
			return nil, e
		}
		if e = t.end(); e != nil {
			return nil, e
		}
		if len(nonce) != 12 || len(opaque) == 0 {
			return nil, errors.New("неверный билет MMTLS")
		}
		out = append(out, mmtlsTicket{byte(kind), uint32(life), uint32(reserved), append([]byte(nil), nonce...), append([]byte(nil), opaque...)})
	}
	if e = c.end(); e != nil {
		return nil, e
	}
	return out, nil
}

func signedHandshakePayload(input []byte, kind byte) ([]byte, error) {
	top := handshakeCursor{input}
	body, e := top.sized(4)
	if e != nil {
		return nil, e
	}
	if e = top.end(); e != nil {
		return nil, e
	}
	c := handshakeCursor{body}
	typ, e := c.uint(1)
	if e != nil {
		return nil, e
	}
	if typ != uint64(kind) {
		return nil, errors.New("неверный тип сообщения handshake")
	}
	value, e := c.sized(2)
	if e != nil {
		return nil, e
	}
	if e = c.end(); e != nil {
		return nil, e
	}
	return append([]byte(nil), value...), nil
}

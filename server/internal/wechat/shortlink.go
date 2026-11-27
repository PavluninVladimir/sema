package wechat

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

func validWeChatHost(host string) bool {
	if len(host) > 253 || !(strings.HasSuffix(host, ".weixin.qq.com") || strings.HasSuffix(host, ".wechat.com")) {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validShortRoute(path string) bool {
	route, ok := strings.CutPrefix(path, "/cgi-bin/micromsg-bin/")
	if !ok || len(route) == 0 || len(route) > 128 {
		return false
	}
	for _, c := range route {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func earlyShortHello(ticket mmtlsTicket, now uint32) (hello, extensions []byte, err error) {
	if len(ticket.nonce) != 12 || len(ticket.opaque) == 0 {
		return nil, nil, errors.New("неверный билет shortlink")
	}
	opaque, err := handshakeSized(ticket.opaque)
	if err != nil {
		return nil, nil, err
	}
	encoded := binary.BigEndian.AppendUint32([]byte{ticket.kind}, ticket.lifetime)
	encoded = append(encoded, 0, 0)
	encoded = binary.BigEndian.AppendUint32(encoded, ticket.reserved)
	encoded = append(encoded, 0, 12)
	encoded = append(encoded, ticket.nonce...)
	encoded = append(encoded, opaque...)
	ticketSection, err := handshakeSection(encoded)
	if err != nil {
		return nil, nil, err
	}
	ext, err := handshakeSection(append([]byte{0, 0x0f, 1}, ticketSection...))
	if err != nil {
		return nil, nil, err
	}
	ext, err = handshakeSection(append([]byte{1}, ext...))
	if err != nil {
		return nil, nil, err
	}
	var randomness [32]byte
	if _, err = rand.Read(randomness[:]); err != nil {
		return nil, nil, err
	}
	body := append([]byte{1, 4, 0xf1, 1, 0, 0xa8}, randomness[:]...)
	body = binary.BigEndian.AppendUint32(body, now)
	hello, err = handshakeSection(append(body, ext...))
	if err != nil {
		return nil, nil, err
	}
	ext, err = handshakeSection(binary.BigEndian.AppendUint32([]byte{0, 0x12}, now))
	if err != nil {
		return nil, nil, err
	}
	ext, err = handshakeSection(append([]byte{1}, ext...))
	if err != nil {
		return nil, nil, err
	}
	extensions, err = handshakeSection(append([]byte{8}, ext...))
	return
}

func prepareShortExchange(s *mmtlsSession, host, path string, payload []byte, now uint32) (hello, extensions, wire []byte, err error) {
	if !validWeChatHost(host) || !validShortRoute(path) || len(payload) > maxMessageBytes || len(s.accessKey) != 32 {
		return nil, nil, nil, errors.New("неверный запрос shortlink")
	}
	hello, extensions, err = earlyShortHello(s.ticket, now)
	if err != nil {
		return nil, nil, nil, err
	}
	keys, err := transcriptKey(s.accessKey, "early data key expansion", hello, 28)
	if err != nil {
		return nil, nil, nil, err
	}
	defer clear(keys)
	route := binary.BigEndian.AppendUint16(nil, uint16(len(path)))
	route = append(route, path...)
	route = binary.BigEndian.AppendUint16(route, uint16(len(host)))
	route = append(route, host...)
	route = binary.BigEndian.AppendUint32(route, uint32(len(payload)))
	route = append(route, payload...)
	route, err = handshakeSection(route)
	if err != nil {
		return nil, nil, nil, err
	}
	wire, err = mmtlsFrame(0x19, hello)
	if err != nil {
		return nil, nil, nil, err
	}
	seal := func(kind byte, body []byte, seq uint64) error {
		next, e := sealMMTLS(kind, body, keys[:16], keys[16:], seq)
		if e == nil {
			wire = append(wire, next...)
		}
		return e
	}
	if err = seal(0x19, extensions, 1); err != nil {
		return nil, nil, nil, err
	}
	seq := uint64(2)
	for len(route) > 0 {
		n := min(len(route), mmtlsMaxPlain)
		if err = seal(0x17, route[:n], seq); err != nil {
			return nil, nil, nil, err
		}
		route = route[n:]
		seq++
	}
	if err = seal(0x15, []byte{0, 0, 0, 3, 0, 1, 1}, seq); err != nil {
		return nil, nil, nil, err
	}
	return
}

func decodeShortReply(input, access, hello, extensions []byte) ([]byte, error) {
	if len(input) > maxMessageBytes || len(access) != 32 {
		return nil, errors.New("неверный размер ответа shortlink")
	}
	reader := bytes.NewReader(input)
	server, err := readMMTLSRecord(reader)
	if err != nil {
		return nil, err
	}
	top := handshakeCursor{server.body}
	body, err := top.sized(4)
	if err != nil || top.end() != nil || server.header[0] != 0x16 || len(body) != 42 ||
		!bytes.Equal(body[:5], []byte{2, 4, 0xf1, 0, 0xa8}) || !bytes.Equal(body[37:], []byte{0, 0, 0, 1, 0}) {
		return nil, errors.New("неверный ServerHello shortlink")
	}
	transcript := make([]byte, 0, len(hello)+len(extensions)+len(server.body))
	transcript = append(transcript, hello...)
	transcript = append(transcript, extensions...)
	transcript = append(transcript, server.body...)
	keys, err := transcriptKey(access, "handshake key expansion", transcript, 28)
	if err != nil {
		return nil, err
	}
	defer clear(keys)
	record, err := readMMTLSRecord(reader)
	if err != nil {
		return nil, err
	}
	if record.header[0] != 0x16 {
		return nil, errors.New("ожидалось Finished shortlink")
	}
	finish, err := openMMTLS(record, keys[:16], keys[16:], 1)
	if err != nil {
		return nil, err
	}
	finished, err := signedHandshakePayload(finish, 0x14)
	if err != nil {
		return nil, err
	}
	expected, err := finishedMAC(access, "server finished", transcript)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(finished, expected) {
		return nil, errors.New("серверный Finished shortlink не прошёл проверку")
	}
	var result []byte
	for seq := uint64(2); reader.Len() > 0; seq++ {
		r, e := readMMTLSRecord(reader)
		if e != nil {
			return nil, e
		}
		data, e := openMMTLS(r, keys[:16], keys[16:], seq)
		if e != nil {
			return nil, e
		}
		switch r.header[0] {
		case 0x17:
			result = append(result, data...)
		case 0x15:
			if seq > 2 && reader.Len() == 0 && bytes.Equal(data, []byte{0, 0, 0, 3, 0, 1, 1}) {
				return result, nil
			}
			return nil, errors.New("неверное завершение shortlink")
		default:
			return nil, errors.New("неожиданная запись shortlink")
		}
	}
	return nil, io.ErrUnexpectedEOF
}

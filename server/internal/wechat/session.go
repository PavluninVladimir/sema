package wechat

import (
	"context"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

// Public verification keys from the upstream MMTLS fixtures at the pinned revision.
var serverPins = map[uint32]string{
	5:    "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEvDq9RQg9ho32ctJGZ1+KtEFksBSF\nEECOCnuhqRPPXQtmvUrUtVvfIdFommkrg/Iyd8vYMdDdtO8GkeoCkJ6FBA==\n-----END PUBLIC KEY-----\n",
	1000: "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEvNO5sjEzAoQpX1WVHmJEM7sNn4d5\nGRix7PLXtaU+wrQ5LgCEwxu6s7hcEUHMMTc9Tc2I+1cuJMaLgST3Q3yKXg==\n-----END PUBLIC KEY-----\n",
}

type mmtlsSession struct {
	region    uint32
	conn      net.Conn
	accessKey []byte
	ticket    mmtlsTicket
	traffic   mmtlsTraffic
}

func (s *mmtlsSession) close() error {
	clear(s.accessKey)
	clear(s.ticket.nonce)
	clear(s.ticket.opaque)
	s.traffic.clear()
	return s.conn.Close()
}

func (t mmtlsTraffic) clear() {
	clear(t.clientKey)
	clear(t.serverKey)
	clear(t.clientIV)
	clear(t.serverIV)
}

func readMMTLSRecord(r io.Reader) (mmtlsRecord, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return mmtlsRecord{}, err
	}
	if header[0] < 0x15 || header[0] > 0x19 || binary.BigEndian.Uint16(header[1:3]) != mmtlsVersion {
		return mmtlsRecord{}, errors.New("неверный заголовок MMTLS")
	}
	record := mmtlsRecord{header: header, body: make([]byte, int(binary.BigEndian.Uint16(header[3:])))}
	_, err := io.ReadFull(r, record.body)
	return record, err
}

func writeMMTLSRecord(w io.Writer, wire []byte) error {
	n, err := w.Write(wire)
	if err == nil && n != len(wire) {
		return io.ErrShortWrite
	}
	return err
}

func makeClientHello(keys [2]*protocolKey, now uint32) ([]byte, error) {
	extension := []byte{0, 0x10, 2}
	for i, key := range keys {
		point, err := handshakeSized(key.public())
		if err != nil {
			return nil, err
		}
		share, err := handshakeSection(append(binary.BigEndian.AppendUint32(nil, uint32(i+5)), point...))
		if err != nil {
			return nil, err
		}
		extension = append(extension, share...)
	}
	extension = append(extension, 0, 0, 0, 0, 2)
	extension = binary.BigEndian.AppendUint32(extension, 5)
	extension = binary.BigEndian.AppendUint32(extension, 1000)
	ext, err := handshakeSection(extension)
	if err != nil {
		return nil, err
	}
	ext, err = handshakeSection(append([]byte{1}, ext...))
	if err != nil {
		return nil, err
	}
	hello := []byte{1, 0x04, 0xf1, 1, 0xc0, 0x2b}
	var randomness [32]byte
	if _, err = rand.Read(randomness[:]); err != nil {
		return nil, err
	}
	hello = append(hello, randomness[:]...)
	hello = binary.BigEndian.AppendUint32(hello, now)
	return handshakeSection(append(hello, ext...))
}

func transcriptKey(secret []byte, label string, transcript []byte, size int) ([]byte, error) {
	digest := sha256.Sum256(transcript)
	return hkdfExpand(secret, append([]byte(label), digest[:]...), size)
}

func finishedMAC(secret []byte, label string, transcript []byte) ([]byte, error) {
	key, err := hkdfExpand(secret, []byte(label), 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	digest := sha256.Sum256(transcript)
	mac := hmac.New(sha256.New, key)
	mac.Write(digest[:])
	return mac.Sum(nil), nil
}

// authenticateMMTLS authenticates the server before returning usable session keys.
func authenticateMMTLS(ctx context.Context, conn net.Conn, pins map[uint32]string) (_ *mmtlsSession, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var keys [2]*protocolKey
	for i := range keys {
		keys[i], err = generateProtocolKey(elliptic.P256())
		if err != nil {
			return nil, err
		}
		defer keys[i].close()
	}
	hello, err := makeClientHello(keys, uint32(time.Now().Unix()))
	if err != nil {
		return nil, err
	}
	frame, err := mmtlsFrame(0x16, hello)
	if err != nil {
		return nil, err
	}
	if err = writeMMTLSRecord(conn, frame); err != nil {
		return nil, err
	}
	first, err := readMMTLSRecord(conn)
	if err != nil {
		return nil, err
	}
	if first.header[0] != 0x16 {
		return nil, errors.New("ожидался ServerHello")
	}
	server, err := parseServerHello(first.body)
	if err != nil {
		return nil, err
	}
	pin := pins[server.version]
	if (server.keyID != 5 && server.keyID != 6) || (server.version != 5 && server.version != 1000) || pin == "" {
		return nil, errors.New("сервер выбрал неизвестный ключ MMTLS")
	}
	shared, err := keys[server.keyID-5].derive(server.point)
	if err != nil {
		return nil, err
	}
	secret := sha256.Sum256(shared)
	clear(shared)
	defer clear(secret[:])
	transcript := append(hello, first.body...)
	handshake, err := deriveMMTLSTraffic(secret[:], "handshake key expansion", transcript)
	if err != nil {
		return nil, err
	}
	defer handshake.clear()

	readProtected := func(kind byte, seq uint64, traffic mmtlsTraffic) ([]byte, error) {
		r, err := readMMTLSRecord(conn)
		if err != nil {
			return nil, err
		}
		if r.header[0] != kind {
			return nil, errors.New("неожиданный тип записи MMTLS")
		}
		return openMMTLS(r, traffic.serverKey, traffic.serverIV, seq)
	}
	signatureRecord, err := readProtected(0x16, 1, handshake)
	if err != nil {
		return nil, err
	}
	signature, err := signedHandshakePayload(signatureRecord, 0x0f)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(transcript)
	if err = verifyPinnedSignature([]byte(pin), digest[:], signature); err != nil {
		return nil, err
	}
	transcript = append(transcript, signatureRecord...)
	ticketRecord, err := readProtected(0x16, 2, handshake)
	if err != nil {
		return nil, err
	}
	defer clear(ticketRecord)
	tickets, err := parseMMTLSTickets(ticketRecord)
	if err != nil {
		return nil, err
	}
	for i := range tickets {
		ticket := tickets[i]
		defer func() {
			if err != nil || i > 0 {
				clear(ticket.nonce)
				clear(ticket.opaque)
			}
		}()
	}
	access, err := transcriptKey(secret[:], "PSK_ACCESS", transcript, 32)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			clear(access)
		}
	}()
	transcript = append(transcript, ticketRecord...)
	defer clear(transcript)

	finishedRecord, err := readProtected(0x16, 3, handshake)
	if err != nil {
		return nil, err
	}
	finished, err := signedHandshakePayload(finishedRecord, 0x14)
	if err != nil {
		return nil, err
	}
	expected, err := finishedMAC(secret[:], "server finished", transcript)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(finished, expected) {
		return nil, errors.New("серверный Finished не прошёл проверку")
	}
	clientFinished, err := finishedMAC(secret[:], "client finished", transcript)
	if err != nil {
		return nil, err
	}
	sized, err := handshakeSized(clientFinished)
	if err != nil {
		return nil, err
	}
	section, err := handshakeSection(append([]byte{0x14}, sized...))
	if err != nil {
		return nil, err
	}
	encrypted, err := sealMMTLS(0x16, section, handshake.clientKey, handshake.clientIV, 1)
	if err != nil {
		return nil, err
	}
	if err = writeMMTLSRecord(conn, encrypted); err != nil {
		return nil, err
	}
	expanded, err := transcriptKey(secret[:], "expanded secret", transcript, 32)
	if err != nil {
		return nil, err
	}
	defer clear(expanded)
	application, err := deriveMMTLSTraffic(expanded, "application data key expansion", transcript)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			application.clear()
		}
	}()
	noop, err := packLonglink(6, ^uint32(0), nil)
	if err != nil {
		return nil, err
	}
	encrypted, err = sealMMTLS(0x17, noop, application.clientKey, application.clientIV, 2)
	if err != nil {
		return nil, err
	}
	if err = writeMMTLSRecord(conn, encrypted); err != nil {
		return nil, err
	}
	response, err := readProtected(0x17, 4, application)
	if err != nil {
		return nil, err
	}
	packet, err := unpackLonglink(response)
	if err != nil {
		return nil, err
	}
	if packet.command != 0x3b9aca06 || packet.sequence != ^uint32(0) || len(packet.payload) != 0 {
		return nil, errors.New("неверный ответ NOOP MMTLS")
	}
	if !stop() {
		return nil, ctx.Err()
	}
	return &mmtlsSession{region: server.region, conn: conn, accessKey: access, ticket: tickets[0], traffic: application}, nil
}

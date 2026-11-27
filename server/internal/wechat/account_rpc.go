package wechat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

type authenticatedPacket struct {
	packet   []byte
	replyKey []byte
	mode     byte
	cgi      uint16
}

func prepareAuthenticated(s *accountState, cgi uint16, plain []byte) (authenticatedPacket, error) {
	var packet authenticatedPacket
	if s == nil || s.UIN == 0 || len(plain) > maxMessageBytes {
		return packet, errors.New("неверный запрос аккаунта WeChat")
	}
	mode, compression := byte(5), byte(2)
	body := plain
	var encrypted, replyKey []byte
	var err error
	switch {
	case len(s.RequestKey) == 24 && len(s.ResponseKey) == 24:
		mode = 13
		replyKey = s.ResponseKey
		encrypted, err = sealRandomEnvelope(plain, s.RequestKey, nil)
	case len(s.SessionKey) == 16 && len(s.RequestKey) == 0 && len(s.ResponseKey) == 0:
		replyKey = s.SessionKey
		compressed, e := deflate(plain)
		if e != nil {
			return packet, e
		}
		defer clear(compressed)
		if len(compressed) < len(plain) {
			body = compressed
			compression = 1
		}
		encrypted, err = legacyCBC(body, s.SessionKey, false)
	default:
		return packet, errors.New("неверные ключи аккаунта WeChat")
	}
	if err != nil {
		return packet, err
	}
	var sequence [2]byte
	if _, err = rand.Read(sequence[:]); err != nil {
		return packet, err
	}
	wire, err := packBusiness(packetPolicy{clientVersion: uint32(s.ClientVersion), uin: s.UIN, cookie: s.Cookie, cgi: cgi,
		originalLength: len(plain), preEncryptionLength: len(body), certVersion: 39, checksum: businessChecksum(s.UIN, plain),
		compression: compression, encryption: mode, noticeID: noticeID(encrypted), sequence: binary.BigEndian.Uint16(sequence[:])}, encrypted)
	if err != nil {
		return packet, err
	}
	return authenticatedPacket{packet: wire, replyKey: replyKey, mode: mode, cgi: cgi}, nil
}

func (p authenticatedPacket) decodeReply(raw []byte) ([]byte, []byte, error) {
	r, err := splitBusiness(raw)
	if err != nil {
		return nil, nil, err
	}
	if r.code != 0 {
		return nil, nil, &serviceError{operation: "обмен", code: r.code}
	}
	if r.cgi != uint64(p.cgi) || r.encryption != p.mode || (r.compression != 1 && r.compression != 2) {
		return nil, nil, errors.New("неверный заголовок ответа аккаунта WeChat")
	}
	var data []byte
	if p.mode == 13 {
		data, err = envelopeOpen(r.payload, p.replyKey, nil)
	} else {
		data, err = legacyCBC(r.payload, p.replyKey, true)
	}
	if err != nil {
		return nil, nil, err
	}
	if len(data) != r.preEncryptionLength {
		clear(data)
		return nil, nil, errors.New("длина ответа аккаунта WeChat не совпадает")
	}
	if r.compression == 1 {
		compressed := data
		data, err = inflate(compressed)
		clear(compressed)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(data) != r.originalLength {
		clear(data)
		return nil, nil, errors.New("длина данных аккаунта WeChat не совпадает")
	}
	return data, r.cookie, nil
}

func (s *mmtlsSession) authenticatedRPC(ctx context.Context, host string, p authenticatedPacket, path string) ([]byte, []byte, error) {
	raw, err := s.exchangeShort(ctx, host, path, p.packet)
	if err != nil {
		return nil, nil, err
	}
	return p.decodeReply(raw)
}

func (s *mmtlsSession) sync(ctx context.Context, account *accountState, cursor []byte, scene uint32) (syncBatch, []byte, error) {
	if scene != 6 && scene != 7 {
		return syncBatch{}, nil, errors.New("неподдерживаемый режим синхронизации WeChat")
	}
	request, err := makeSyncRequest(cursor, account.DeviceKind, scene)
	if err != nil {
		return syncBatch{}, nil, err
	}
	packet, err := prepareAuthenticated(account, 138, request)
	if err != nil {
		return syncBatch{}, nil, err
	}
	raw, cookie, err := s.authenticatedRPC(ctx, account.ShortHost, packet, "/cgi-bin/micromsg-bin/newsync")
	if err != nil {
		return syncBatch{}, nil, err
	}
	batch, err := decodeSync(raw, cursor)
	return batch, cookie, err
}

var errDeliveryUnknown = errors.New("доставка сообщения WeChat не подтверждена")

func (s *mmtlsSession) sendText(ctx context.Context, account *accountState, destination, text string, timestamp, clientID uint32) (textSendResult, []byte, error) {
	request, err := makeTextSend(destination, text, timestamp, clientID)
	if err != nil {
		return textSendResult{}, nil, err
	}
	packet, err := prepareAuthenticated(account, 522, request)
	if err != nil {
		return textSendResult{}, nil, err
	}
	raw, cookie, err := s.authenticatedRPC(ctx, account.ShortHost, packet, "/cgi-bin/micromsg-bin/newsendmsg")
	if err != nil {
		var rejected *serviceError
		if errors.As(err, &rejected) {
			return textSendResult{code: rejected.code}, nil, nil
		}
		return textSendResult{}, nil, fmt.Errorf("%w: %v", errDeliveryUnknown, err)
	}
	result, err := decodeTextSend(raw, destination, clientID)
	if err != nil {
		return textSendResult{}, cookie, fmt.Errorf("%w: %v", errDeliveryUnknown, err)
	}
	return result, cookie, nil
}

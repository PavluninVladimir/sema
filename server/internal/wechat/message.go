package wechat

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

type receivedMessage struct {
	peer, sender, serverID, archiveID string
	outgoing                          bool
	kind                              int32
	created                           uint32
	content                           []byte
}

func decodeMessage(input []byte, own string) (receivedMessage, error) {
	var message receivedMessage
	fields, err := decodeFields(input)
	if err != nil {
		return message, err
	}
	nested := func(number uint32) ([]byte, error) {
		raw, e := dataField(fields, number)
		if e != nil {
			return nil, e
		}
		f, e := decodeFields(raw)
		if e != nil {
			return nil, e
		}
		return optionalBytes(f, 1)
	}
	fromData, err := nested(2)
	if err != nil {
		return message, err
	}
	toData, err := nested(3)
	if err != nil {
		return message, err
	}
	if own == "" || len(fromData) == 0 || len(toData) == 0 || len(fromData) > 512 || len(toData) > 512 ||
		!utf8.Valid(fromData) || !utf8.Valid(toData) {
		return message, errors.New("неверные участники сообщения WeChat")
	}
	from, to := string(fromData), string(toData)
	if strings.ContainsRune(from, 0) || strings.ContainsRune(to, 0) {
		return message, errors.New("неверные имена участников WeChat")
	}
	rawKind, err := scalarField(fields, 4)
	if err != nil {
		return message, err
	}
	kind := int32(uint32(rawKind))
	peer := from
	outgoing := from == own
	if outgoing && kind != 10000 {
		peer = to
	}
	if from != own && to != own && !strings.HasSuffix(peer, "@chatroom") {
		return message, errors.New("сообщение относится к другому аккаунту WeChat")
	}
	content, err := nested(5)
	if err != nil {
		return message, err
	}
	at, err := readUint32(fields, 9)
	if err != nil {
		return message, err
	}
	id, err := optionalField(fields, 12)
	if err != nil {
		return message, err
	}
	var serverID string
	if id != nil {
		if id.kind != 0 {
			return message, errors.New("неверный идентификатор сообщения WeChat")
		}
		if id.scalar != 0 {
			serverID = strconv.FormatUint(id.scalar, 10)
		}
	}
	archiveID := "server:" + serverID
	if serverID == "" {
		fingerprint := sha256.Sum256(input)
		archiveID = "raw:" + hex.EncodeToString(fingerprint[:])
	}
	sender := from
	if strings.HasSuffix(from, "@chatroom") {
		sender = ""
	}
	return receivedMessage{peer: peer, sender: sender, serverID: serverID, archiveID: archiveID, outgoing: outgoing, kind: kind, created: at, content: content}, nil
}

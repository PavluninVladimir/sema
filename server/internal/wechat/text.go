package wechat

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

func makeTextSend(destination, text string, timestamp, clientID uint32) ([]byte, error) {
	if len(destination) == 0 || len(destination) > 256 || len(text) == 0 || len(text) > 64*1024 ||
		!utf8.ValidString(destination) || !utf8.ValidString(text) || strings.ContainsRune(destination, 0) ||
		strings.ContainsRune(text, 0) || timestamp == 0 || clientID == 0 {
		return nil, errors.New("неверные параметры сообщения WeChat")
	}
	dest, _ := appendBytes(nil, 1, []byte(destination))
	item, _ := appendBytes(nil, 1, dest)
	item, _ = appendBytes(item, 2, []byte(text))
	item, _ = appendUint(item, 3, 1)
	item, _ = appendUint(item, 4, uint64(timestamp))
	item, _ = appendUint(item, 5, uint64(clientID))
	request, _ := appendUint(nil, 1, 1)
	return appendBytes(request, 2, item)
}

type textSendResult struct {
	code     int32
	serverID string
}

// A malformed reply leaves the delivery status unknown. Only an authenticated,
// matching server reply can confirm or reject the submitted message.
func decodeTextSend(input []byte, destination string, clientID uint32) (textSendResult, error) {
	var result textSendResult
	f, err := decodeFields(input)
	if err != nil {
		return result, err
	}
	code, err := baseCode(f)
	if err != nil {
		return result, err
	}
	count, err := scalarField(f, 2)
	if err != nil {
		return result, err
	}
	if code != 0 {
		return textSendResult{code: code}, nil
	}
	raw, err := dataField(f, 3)
	if err != nil || count != 1 {
		return result, errors.New("неверное число результатов отправки WeChat")
	}
	item, err := decodeFields(raw)
	if err != nil {
		return result, err
	}
	status, err := scalarField(item, 1)
	if err != nil {
		return result, err
	}
	returned, err := dataField(item, 2)
	if err != nil {
		return result, err
	}
	peer, err := decodeFields(returned)
	if err != nil {
		return result, err
	}
	target, err := dataField(peer, 1)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(target, []byte(destination)) {
		return result, errors.New("ответ относится к другому чату WeChat")
	}
	for n := uint32(3); n <= 7; n++ {
		value, err := scalarField(item, n)
		if err != nil || value > uint64(^uint32(0)) || (n == 4 && value != uint64(clientID)) {
			return result, errors.New("поля ответа не совпадают с отправкой WeChat")
		}
	}
	result.code = int32(uint32(status))
	if result.code != 0 {
		return result, nil
	}
	id, err := scalarField(item, 8)
	if err != nil || id == 0 {
		return textSendResult{}, errors.New("нет идентификатора отправленного сообщения WeChat")
	}
	result.serverID = strconv.FormatUint(id, 10)
	return result, nil
}

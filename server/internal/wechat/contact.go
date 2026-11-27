package wechat

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Contact struct {
	Peer, Title string
}

func decodeContact(input []byte) (Contact, error) {
	var out Contact
	fields, err := decodeFields(input)
	if err != nil {
		return out, err
	}
	text := func(n uint32) (string, error) {
		v, err := optionalBytes(fields, n)
		if err != nil || v == nil {
			return "", err
		}
		nested, err := decodeFields(v)
		if err != nil {
			return "", err
		}
		raw, err := optionalBytes(nested, 1)
		if err != nil {
			return "", err
		}
		if len(raw) > 1024 || !utf8.Valid(raw) || strings.ContainsFunc(string(raw), unicode.IsControl) {
			return "", errors.New("неверное поле контакта WeChat")
		}
		return string(raw), nil
	}
	peer, err := text(1)
	if err != nil || peer == "" || len(peer) > 512 || strings.ContainsFunc(peer, unicode.IsSpace) {
		return out, errors.New("неверный идентификатор контакта WeChat")
	}
	title, err := text(10)
	if err != nil {
		return out, err
	}
	if title == "" {
		title, err = text(2)
	}
	if err != nil {
		return out, err
	}
	return Contact{Peer: peer, Title: title}, nil
}

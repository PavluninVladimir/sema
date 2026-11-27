package wechat

import (
	"bytes"
	"context"
	"errors"
)

const contactSelector = 0x20004

type ContactPage struct {
	Cursor   []byte
	Contacts []Contact
	More     bool
}

func validateContactCursor(cursor []byte) error {
	if len(cursor) > 65536 {
		return errors.New("слишком большой курсор контактов WeChat")
	}
	fields, err := decodeFields(cursor)
	if err != nil {
		return err
	}
	keys := make(map[uint32]bool)
	for _, field := range fields {
		if field.number != 1 {
			continue
		}
		if field.kind != 2 || len(keys) >= 256 {
			return errors.New("неверный состав курсора контактов WeChat")
		}
		pair, err := decodeFields(field.data)
		if err != nil {
			return err
		}
		key, err := readUint32(pair, 1)
		if err != nil || keys[key] {
			return errors.New("неверный ключ курсора контактов WeChat")
		}
		if _, err := scalarField(pair, 2); err != nil {
			return err
		}
		keys[key] = true
	}
	return nil
}

func makeContactRequest(account *accountState, cursor, messageCursor []byte, continuing bool) ([]byte, error) {
	if len(messageCursor) > 65536 || len(cursor)+len(messageCursor) == 0 {
		return nil, errors.New("нет позиции для синхронизации контактов WeChat")
	}
	if err := validateContactCursor(cursor); err != nil {
		return nil, err
	}
	base, err := baseRequest(loginDevice{id: account.DeviceID, kind: account.DeviceKind, version: account.ClientVersion}, account.UIN, 0)
	if err != nil {
		return nil, err
	}
	scene := uint64(3)
	if continuing {
		scene = 4
	}
	b, _ := appendBytes(nil, 1, base)
	b, _ = appendUint(b, 2, scene)
	b, _ = appendUint(b, 3, contactSelector)
	b, _ = appendBytes(b, 4, cursor)
	return appendBytes(b, 5, messageCursor)
}

func decodeContactPage(raw []byte, prior []byte) (ContactPage, error) {
	fields, err := decodeFields(raw)
	if err != nil {
		return ContactPage{}, err
	}
	code, err := baseCode(fields)
	if err != nil {
		return ContactPage{}, err
	}
	if code != 0 {
		return ContactPage{}, &serviceError{operation: "синхронизация контактов", code: code}
	}
	cursor, err := dataField(fields, 3)
	if err != nil {
		return ContactPage{}, err
	}
	if err := validateContactCursor(cursor); err != nil {
		return ContactPage{}, err
	}
	page := ContactPage{Cursor: cursor}
	selectors, commands := 0, 0
	for _, f := range fields {
		switch f.number {
		case 2:
			selectors++
			if f.kind != 0 || selectors > 64 {
				return ContactPage{}, errors.New("неверное продолжение списка контактов WeChat")
			}
			page.More = page.More || f.scalar != 0
		case 4:
			commands++
			if f.kind != 2 || commands > 10000 {
				return ContactPage{}, errors.New("неверный список команд контактов WeChat")
			}
			item, err := decodeFields(f.data)
			if err != nil {
				return ContactPage{}, err
			}
			id, err := readUint32(item, 1)
			if err != nil {
				return ContactPage{}, err
			}
			if id != 2 {
				continue
			}
			payload, err := dataField(item, 2)
			if err != nil {
				return ContactPage{}, err
			}
			contact, err := decodeContact(payload)
			if err != nil {
				return ContactPage{}, err
			}
			page.Contacts = append(page.Contacts, contact)
		}
	}
	if page.More && bytes.Equal(prior, page.Cursor) {
		return ContactPage{}, errors.New("курсор контактов WeChat не продвинулся")
	}
	return page, nil
}

// PullContacts reads one page of the independent contact stream (CGI 8674).
func PullContacts(ctx context.Context, snapshot, cursor, messageCursor []byte, continuing bool) (ContactPage, []byte, error) {
	state, err := readSecretSnapshot(snapshot)
	if err != nil {
		return ContactPage{}, nil, err
	}
	defer state.close()
	request, err := makeContactRequest(state, cursor, messageCursor, continuing)
	if err != nil {
		return ContactPage{}, nil, err
	}
	packet, err := prepareAuthenticated(state, 8674, request)
	if err != nil {
		return ContactPage{}, nil, err
	}
	session, err := dialMMTLS(ctx, state.LongHost)
	if err != nil {
		return ContactPage{}, nil, err
	}
	defer session.close()
	raw, cookie, err := session.authenticatedRPC(ctx, state.ShortHost, packet, "/cgi-bin/micromsg-bin/contactsync")
	if err != nil {
		return ContactPage{}, nil, err
	}
	defer clear(raw)
	page, err := decodeContactPage(raw, cursor)
	if err != nil || len(cookie) == 0 {
		return page, nil, err
	}
	clear(state.Cookie)
	state.Cookie = append([]byte(nil), cookie...)
	updated, err := state.secretSnapshot()
	return page, updated, err
}

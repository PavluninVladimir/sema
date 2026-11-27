package wechat

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/md5"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

type accountState struct {
	LongHost, ShortHost                                               string
	UIN                                                               uint32
	Username                                                          string
	Cookie, SessionKey, RequestKey, ResponseKey, AutoAuthKey, KeyInfo []byte
	DeviceID                                                          []byte
	DeviceKind                                                        string
	ClientVersion                                                     int32
}

func (s *accountState) close() {
	for _, secret := range [][]byte{s.Cookie, s.SessionKey, s.RequestKey, s.ResponseKey, s.AutoAuthKey, s.KeyInfo} {
		clear(secret)
	}
}

func makeManualAuth(device loginDevice, name string, poll loginPoll, point []byte) ([]byte, error) {
	if poll.status == nil || *poll.status != 2 || poll.lanChallenge || len(poll.credential3) == 0 || len(poll.credential4) == 0 ||
		len(poll.credential3) > 65536 || len(poll.credential4) > 65536 || len(point) != 57 || point[0] != 4 ||
		len(name) == 0 || len(name) > 255 || !utf8.ValidString(name) {
		return nil, errors.New("вход WeChat ещё не подтверждён")
	}
	base, err := baseRequest(device, 0, 1)
	if err != nil {
		return nil, err
	}
	nonce, err := loginNonceBuffer()
	if err != nil {
		return nil, err
	}
	pub, _ := appendUint(nil, 1, 713)
	pointBuf, _ := packBuffer(point)
	pub, _ = appendBytes(pub, 2, pointBuf)
	first, _ := appendBytes(nil, 1, nonce)
	first, _ = appendBytes(first, 2, pub)
	first, _ = appendBytes(first, 3, poll.credential3)
	first, _ = appendBytes(first, 4, poll.credential4)
	first, _ = appendBytes(first, 5, nil)
	zero, _ := packBuffer([]byte{0})
	inner, _ := appendBytes(nil, 1, nil)
	inner, _ = appendBytes(inner, 4, zero)
	secondWrapper, _ := appendBytes(nil, 2, inner)
	second, _ := appendBytes(nil, 1, base)
	second, _ = appendBytes(second, 2, secondWrapper)
	second, _ = appendUint(second, 5, 0)
	second, _ = appendBytes(second, 8, []byte(name))
	second, _ = appendBytes(second, 9, []byte(device.kind))
	second, _ = appendBytes(second, 11, nil)
	second, _ = appendUint(second, 13, 1000)
	second, _ = appendBytes(second, 19, []byte("0"))
	second, _ = appendUint(second, 22, 2)
	out, _ := appendBytes(nil, 1, first)
	return appendBytes(out, 2, second)
}

func initialAccount(input, cookie []byte, device loginDevice, inner *protocolKey) (_ *accountState, err error) {
	if len(input) > 1024*1024 || len(cookie) > 15 {
		return nil, errors.New("неверный размер ответа входа WeChat")
	}
	f, err := loginResponseFields(input, "подтверждение входа")
	if err != nil {
		return nil, err
	}
	mask, err := scalarField(f, 2)
	if err != nil || mask > uint64(^uint32(0)) || mask&1 == 0 {
		return nil, errors.New("в ответе входа отсутствует секция ключей")
	}
	for bit := uint64(1); bit <= 4; bit <<= 1 {
		if mask&bit != 0 {
			number := uint32(3)
			if bit == 2 {
				number = 4
			} else if bit == 4 {
				number = 5
			}
			if _, err = dataField(f, number); err != nil {
				return nil, err
			}
		}
	}
	auth, err := dataField(f, 3)
	if err != nil {
		return nil, err
	}
	a, err := decodeFields(auth)
	if err != nil {
		return nil, err
	}
	account, err := dataField(f, 4)
	if err != nil {
		return nil, err
	}
	identity, err := decodeFields(account)
	if err != nil {
		return nil, err
	}
	name, err := dataField(identity, 1)
	if err != nil || len(name) == 0 || len(name) > 1024 || !utf8.Valid(name) {
		return nil, errors.New("неверное имя аккаунта WeChat")
	}
	uin, err := scalarField(a, 1)
	if err != nil || uin == 0 || uin > uint64(^uint32(0)) {
		return nil, errors.New("неверный идентификатор аккаунта WeChat")
	}
	state := &accountState{UIN: uint32(uin), Username: string(name), Cookie: append([]byte(nil), cookie...), DeviceID: append([]byte(nil), device.id...), DeviceKind: device.kind, ClientVersion: device.version}
	defer func() {
		if err != nil {
			state.close()
		}
	}()
	readBuffer := func(n uint32) ([]byte, error) {
		data, err := optionalBytes(a, n)
		if err != nil || data == nil {
			return nil, err
		}
		return unpackBuffer(data)
	}
	state.SessionKey, err = readBuffer(3)
	if err != nil {
		return nil, err
	}
	flags, err := optionalField(a, 19)
	if err != nil {
		return nil, err
	}
	if flags != nil {
		if flags.kind != 0 {
			return nil, errors.New("неверные флаги входа")
		}
		if flags.scalar&4 != 0 {
			key, err := dataField(a, 2)
			if err != nil {
				return nil, err
			}
			fields, err := decodeFields(key)
			if err != nil {
				return nil, err
			}
			curve, err := scalarField(fields, 1)
			if err != nil || curve != 713 {
				return nil, errors.New("неподдерживаемая кривая аккаунта")
			}
			wrapped, err := dataField(fields, 2)
			if err != nil {
				return nil, err
			}
			point, err := unpackBuffer(wrapped)
			if err != nil {
				return nil, err
			}
			shared, err := inner.derive(point)
			if err != nil {
				return nil, err
			}
			keyHash := md5.Sum(shared)
			clear(shared)
			raw := state.SessionKey
			state.SessionKey, err = legacyCBC(raw, keyHash[:], true)
			clear(raw)
			clear(keyHash[:])
			if err != nil {
				return nil, err
			}
		}
	}
	for n, target := range map[uint32]*[]byte{4: &state.AutoAuthKey, 10: &state.KeyInfo, 23: &state.RequestKey, 24: &state.ResponseKey} {
		*target, err = readBuffer(n)
		if err != nil {
			return nil, err
		}
	}
	if len(state.SessionKey) == 0 || len(state.SessionKey) > 65536 ||
		!((len(state.RequestKey) == 24 && len(state.ResponseKey) == 24) || (len(state.RequestKey) == 0 && len(state.ResponseKey) == 0 && len(state.SessionKey) == 16)) {
		return nil, errors.New("неверный набор ключей аккаунта WeChat")
	}
	return state, nil
}

// secretSnapshot is intended for the encrypted secrets store only.
func (s *accountState) secretSnapshot() ([]byte, error) { return json.Marshal(s) }

func (a *qrAttempt) complete(ctx context.Context, name string, poll loginPoll) (*accountState, error) {
	inner, err := generateProtocolKey(elliptic.P224())
	if err != nil {
		return nil, err
	}
	defer inner.close()
	plain, err := makeManualAuth(a.device, name, poll, inner.public())
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	long, short := defaultLongHost, defaultShortHost
	cookie := a.cookie
	session := a.session
	for retry := 0; retry < 2; retry++ {
		reply, nextCookie, err := session.preloginRPC(ctx, short, 252, "/cgi-bin/micromsg-bin/secmanualauth", plain, cookie, nil)
		if err != nil {
			return nil, err
		}
		defer clear(reply)
		defer clear(nextCookie)
		fields, err := decodeFields(reply)
		if err != nil {
			return nil, err
		}
		code, err := baseCode(fields)
		if err != nil {
			return nil, err
		}
		if code == -301 && retry == 0 {
			long, short, err = authRedirect(fields)
			if err != nil {
				return nil, err
			}
			session, err = dialMMTLS(ctx, long)
			if err != nil {
				return nil, err
			}
			defer session.close()
			cookie = nextCookie
			continue
		}
		state, err := initialAccount(reply, nextCookie, a.device, inner)
		if err != nil {
			return nil, err
		}
		state.LongHost, state.ShortHost = long, short
		return state, nil
	}
	return nil, errors.New("WeChat повторно перенаправил вход")
}

func authRedirect(fields []wireField) (string, string, error) {
	raw, err := dataField(fields, 5)
	if err != nil {
		return "", "", err
	}
	network, err := decodeFields(raw)
	if err != nil {
		return "", "", err
	}
	raw, err = dataField(network, 1)
	if err != nil {
		return "", "", err
	}
	hosts, err := decodeFields(raw)
	if err != nil {
		return "", "", err
	}
	targets := map[string]string{}
	for _, entry := range hosts {
		if entry.number != 2 {
			continue
		}
		if entry.kind != 2 {
			return "", "", errors.New("неверный адрес перенаправления WeChat")
		}
		host, err := decodeFields(entry.data)
		if err != nil {
			return "", "", err
		}
		from, err := dataField(host, 1)
		if err != nil {
			return "", "", err
		}
		if !bytes.Equal(from, []byte(defaultLongHost)) && !bytes.Equal(from, []byte(defaultShortHost)) {
			continue
		}
		dest, err := dataField(host, 2)
		if err != nil {
			return "", "", err
		}
		if !validWeChatHost(string(dest)) || targets[string(from)] != "" {
			return "", "", errors.New("недопустимое перенаправление WeChat")
		}
		targets[string(from)] = string(dest)
	}
	long, short := targets[defaultLongHost], targets[defaultShortHost]
	if long == "" || short == "" {
		return "", "", errors.New("WeChat не выдал адреса перенаправления")
	}
	return long, short, nil
}

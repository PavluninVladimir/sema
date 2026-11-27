package wechat

import (
	"context"
	"crypto/elliptic"
	"errors"
)

const sessionRejectedCode = -13

func makeAutoAuth(state *accountState, point []byte) ([]byte, error) {
	token, err := decodeFields(state.AutoAuthKey)
	if err != nil {
		return nil, err
	}
	first, err := dataField(token, 1)
	if err != nil {
		return nil, err
	}
	first, err = unpackBuffer(first)
	if err != nil || len(first) != 32 {
		return nil, errors.New("неверный ключ продления сессии WeChat")
	}
	defer clear(first)
	second, err := dataField(token, 2)
	if err != nil {
		return nil, err
	}
	second, err = unpackBuffer(second)
	if err != nil || len(second) == 0 {
		return nil, errors.New("неполный ключ продления сессии WeChat")
	}
	defer clear(second)
	if len(point) != 57 || point[0] != 4 {
		return nil, errors.New("неверная точка продления сессии WeChat")
	}
	base, err := baseRequest(loginDevice{id: state.DeviceID, kind: state.DeviceKind, version: state.ClientVersion}, state.UIN, 12)
	if err != nil {
		return nil, err
	}
	zero, _ := packBuffer([]byte{0})
	wt, _ := appendBytes(nil, 4, zero)
	auth, _ := appendBytes(nil, 2, wt)
	if len(state.KeyInfo) > 0 {
		buf, err := packBuffer(state.KeyInfo)
		if err != nil {
			return nil, err
		}
		auth, err = appendBytes(auth, 5, buf)
		if err != nil {
			return nil, err
		}
	}
	aes, _ := appendBytes(nil, 1, base)
	aes, _ = appendBytes(aes, 2, auth)
	saved, err := packBuffer(state.AutoAuthKey)
	if err != nil {
		return nil, err
	}
	aes, err = appendBytes(aes, 3, saved)
	if err != nil {
		return nil, err
	}
	aes, _ = appendUint(aes, 6, 0)
	aes, _ = appendBytes(aes, 9, []byte("Sema"))
	aes, _ = appendBytes(aes, 10, []byte(state.DeviceKind))
	aes, _ = appendBytes(aes, 11, []byte("en"))
	aes, _ = appendBytes(aes, 12, nil)
	aes, _ = appendBytes(aes, 19, []byte(state.DeviceKind))

	pub, _ := appendUint(nil, 1, 713)
	pointBuf, _ := packBuffer(point)
	pub, _ = appendBytes(pub, 2, pointBuf)
	firstBuf, _ := packBuffer(first)
	rsa, _ := appendBytes(nil, 2, firstBuf)
	rsa, _ = appendBytes(rsa, 3, pub)
	out, _ := appendBytes(nil, 1, rsa)
	return appendBytes(out, 2, aes)
}

// Renew replaces a stored session only if WeChat confirms the same account.
func Renew(ctx context.Context, snapshot []byte) ([]byte, error) {
	state, err := readSecretSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	defer state.close()
	if len(state.AutoAuthKey) < 24 {
		return nil, errors.New("у WeChat нет ключа продления сессии. Подключите аккаунт заново")
	}
	inner, err := generateProtocolKey(elliptic.P224())
	if err != nil {
		return nil, err
	}
	defer inner.close()
	request, err := makeAutoAuth(state, inner.public())
	if err != nil {
		return nil, err
	}
	defer clear(request)
	long, short := state.LongHost, state.ShortHost
	cookie := state.Cookie
	for retry := 0; retry < 2; retry++ {
		session, err := dialMMTLS(ctx, long)
		if err != nil {
			return nil, err
		}
		reply, nextCookie, err := session.preloginRPC(ctx, short, 763, "/cgi-bin/micromsg-bin/secautoauth", request, cookie, state.AutoAuthKey)
		session.close()
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
			cookie = nextCookie
			continue
		}
		if len(nextCookie) == 0 {
			nextCookie = cookie
		}
		next, err := initialAccount(reply, nextCookie, loginDevice{id: state.DeviceID, kind: state.DeviceKind, version: state.ClientVersion}, inner)
		if err != nil {
			return nil, err
		}
		defer next.close()
		if next.UIN != state.UIN || next.Username != state.Username {
			return nil, errors.New("при продлении изменился аккаунт WeChat")
		}
		next.LongHost, next.ShortHost = long, short
		return next.secretSnapshot()
	}
	return nil, errors.New("WeChat повторно перенаправил продление сессии")
}

func SessionRejected(err error) bool {
	var service *serviceError
	return errors.As(err, &service) && service.code == sessionRejectedCode
}

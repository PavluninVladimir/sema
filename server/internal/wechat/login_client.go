package wechat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"time"
)

// Public business keys from the upstream fixtures at the pinned revision.
var businessPins = map[uint32]struct {
	version    uint16
	point, pem string
}{
	1: {10014, "0485140cd4892095aecbf79e71cef6348a92eb9b3bbc697e38ab11bd1187cca8e749623dc33c94e0d5166c6de42018258edaf6128ec08a9548927e60c29ed46116", `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAECh6rXlsE2tI0kPePrkGdqfax3fM4
eqr7vmerVCTDizLW9FVby/9Uz3wxV5IQXjBT9JnLSNrhwPG/CbufQGwMvQ==
-----END PUBLIC KEY-----
`},
	2: {50005, "0476a400ef255ef6729aa837be190b3d30832deb78e4fad786c2e94ecc6b0e8ca69f8f585726c6422bb953067edbf2e907918716ba55175d4e2c167c41aa709558", `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEKayuW6LOC4vQ74qVRu25yeDLP6v2
SqBIIdoY7iFk2k89NLjCAST9imZBpPUgHGeyDVWBvgIRHDoVW3zAfAWTKA==
-----END PUBLIC KEY-----
`},
}

const defaultLongHost = "long.weixin.qq.com"
const defaultShortHost = "short.weixin.qq.com"

func (s *mmtlsSession) preloginRPC(ctx context.Context, host string, cgi uint16, path string, plain, cookie, authKey []byte) ([]byte, []byte, error) {
	pin, ok := businessPins[s.region]
	if !ok {
		return nil, nil, errors.New("неподдерживаемый регион ключа WeChat")
	}
	point, err := hex.DecodeString(pin.point)
	if err != nil {
		return nil, nil, err
	}
	login, err := preparePrelogin(plain, businessProfile{pin.version, point, []byte(pin.pem)}, authKey)
	if err != nil {
		return nil, nil, err
	}
	defer login.close()
	var seq [2]byte
	if _, err = rand.Read(seq[:]); err != nil {
		return nil, nil, err
	}
	packet, err := packBusiness(packetPolicy{
		cookie: cookie, cgi: cgi, originalLength: len(plain), preEncryptionLength: len(plain),
		keyVersion: pin.version, compression: 2, encryption: 12, noticeID: noticeID(login.request),
		sequence: binary.BigEndian.Uint16(seq[:]),
	}, login.request)
	if err != nil {
		return nil, nil, err
	}
	raw, err := s.exchangeShort(ctx, host, path, packet)
	if err != nil {
		return nil, nil, err
	}
	reply, err := splitBusiness(raw)
	if err != nil {
		return nil, nil, err
	}
	if reply.code != 0 {
		return nil, nil, &serviceError{operation: "вход", code: reply.code}
	}
	if reply.cgi != uint64(cgi) || reply.encryption != 12 {
		return nil, nil, errors.New("неверный ответ на запрос входа WeChat")
	}
	result, err := login.decryptReply(reply.payload)
	if err != nil {
		return nil, nil, err
	}
	return result, reply.cookie, nil
}

type qrAttempt struct {
	qr      loginQR
	cookie  []byte
	device  loginDevice
	expires time.Time
	session *mmtlsSession
	lan     *lanChallenge
}

func startQRLogin(ctx context.Context, device loginDevice, name string) (_ *qrAttempt, err error) {
	plain, err := makeQRRequest(device, name)
	if err != nil {
		return nil, err
	}
	session, err := dialMMTLS(ctx, defaultLongHost)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			session.close()
		}
	}()
	reply, cookie, err := session.preloginRPC(ctx, defaultShortHost, 502, "/cgi-bin/micromsg-bin/getloginqrcode", plain, nil, nil)
	if err != nil {
		return nil, err
	}
	defer clear(reply)
	qr, err := decodeLoginQR(reply)
	if err != nil {
		return nil, err
	}
	device.id = append([]byte(nil), device.id...)
	return &qrAttempt{qr: qr, cookie: cookie, device: device, expires: time.Now().Add(4 * time.Minute), session: session}, nil
}

func (a *qrAttempt) close() error {
	a.closeLAN()
	clear(a.cookie)
	clear(a.qr.notifyKey)
	clear(a.qr.identifier)
	clear(a.qr.image)
	return a.session.close()
}

func (a *qrAttempt) closeLAN() {
	if a.lan != nil {
		a.lan.close()
		a.lan = nil
	}
}

func (a *qrAttempt) poll(ctx context.Context) (loginPoll, error) {
	now := time.Now()
	if !now.Before(a.expires) {
		return loginPoll{}, errors.New("срок действия QR WeChat истёк")
	}
	plain, err := makePollRequest(a.device, a.qr.identifier, now)
	if err != nil {
		return loginPoll{}, err
	}
	reply, cookie, err := a.session.preloginRPC(ctx, defaultShortHost, 503, "/cgi-bin/micromsg-bin/checkloginqrcode", plain, a.cookie, nil)
	if err != nil {
		return loginPoll{}, err
	}
	defer clear(reply)
	result, err := decodeLoginPoll(reply, a.qr.notifyKey)
	if err != nil {
		return loginPoll{}, err
	}
	if len(cookie) != 0 {
		clear(a.cookie)
		a.cookie = cookie
	}
	return result, nil
}

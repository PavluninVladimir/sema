package wechat

import (
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strconv"
)

type businessProfile struct {
	version      uint16
	point        []byte
	signaturePEM []byte
}

type prelogin struct {
	request                   []byte
	local                     *protocolKey
	plain, derived, encrypted []byte
	pin                       []byte
	hasAuth                   bool
}

func (p *prelogin) close() {
	p.local.close()
	clear(p.plain)
	clear(p.derived)
}

func hashParts(parts ...[]byte) []byte {
	digest := sha256.New()
	for _, p := range parts {
		digest.Write(p)
	}
	return digest.Sum(nil)
}

func sealRandomEnvelope(plain, key, aad []byte) ([]byte, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return envelopeSeal(plain, key, aad, nonce[:])
}

func preparePrelogin(plain []byte, profile businessProfile, auth []byte) (_ *prelogin, err error) {
	if len(plain) > maxMessageBytes || len(profile.point) != 65 || len(profile.signaturePEM) == 0 ||
		(len(auth) != 0 && len(auth) < 24) {
		return nil, errors.New("неверные параметры входа WeChat")
	}
	local, err := generateProtocolKey(elliptic.P256())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			local.close()
		}
	}()
	point := local.public()
	prefix := append([]byte("1415"), point...)
	aad := hashParts(prefix)
	shared, err := local.derive(profile.point)
	if err != nil {
		return nil, err
	}
	sharedKey := sha256.Sum256(shared)
	clear(shared)
	defer clear(sharedKey[:])
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	defer clear(nonce[:])
	encryptedRandom, err := sealRandomEnvelope(nonce[:], sharedKey[:24], aad)
	if err != nil {
		return nil, err
	}
	var authBlock []byte
	if len(auth) > 0 {
		authBlock, err = sealRandomEnvelope(nonce[:], auth[:24], aad)
		if err != nil {
			return nil, err
		}
	}
	mac := hmac.New(sha256.New, []byte("security hdkf expand"))
	mac.Write(nonce[:])
	extracted := mac.Sum(nil)
	defer clear(extracted)
	derived, err := hkdfExpand(extracted, aad, 56)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			clear(derived)
		}
	}()
	encrypted, err := sealRandomEnvelope(plain, derived[:24], hashParts(prefix, encryptedRandom, authBlock))
	if err != nil {
		return nil, err
	}
	pub, _ := appendUint(nil, 1, 415)
	pub, _ = appendBytes(pub, 2, point)
	request, _ := appendUint(nil, 1, 1)
	for i, b := range [][]byte{pub, encryptedRandom, authBlock, encrypted} {
		request, err = appendBytes(request, uint32(i+2), b)
		if err != nil {
			return nil, err
		}
	}
	return &prelogin{request: request, local: local, plain: append([]byte(nil), plain...), derived: derived,
		encrypted: encrypted, pin: append([]byte(nil), profile.signaturePEM...), hasAuth: len(auth) > 0}, nil
}

func (p *prelogin) decryptReply(reply []byte) ([]byte, error) {
	fields, err := decodeFields(reply)
	if err != nil {
		return nil, err
	}
	key, err := dataField(fields, 1)
	if err != nil {
		return nil, err
	}
	keyFields, err := decodeFields(key)
	if err != nil {
		return nil, err
	}
	curve, err := scalarField(keyFields, 1)
	if err != nil || curve != 415 {
		return nil, errors.New("неподдерживаемая кривая входа WeChat")
	}
	point, err := dataField(keyFields, 2)
	if err != nil {
		return nil, err
	}
	credential, err := scalarField(fields, 2)
	if err != nil || len(point) != 65 || point[0] != 4 || (credential != 1 && credential != 2) || (credential == 2 && !p.hasAuth) {
		return nil, errors.New("неподдерживаемое подтверждение WeChat")
	}
	encrypted, err := dataField(fields, 3)
	if err != nil {
		return nil, err
	}
	if credential == 1 {
		signature, err := dataField(fields, 4)
		if err != nil {
			return nil, err
		}
		if err = verifyPinnedSignature(p.pin, hashParts(p.encrypted, encrypted), signature); err != nil {
			return nil, err
		}
	}
	shared, err := p.local.derive(point)
	if err != nil {
		return nil, err
	}
	sharedKey := sha256.Sum256(shared)
	clear(shared)
	defer clear(sharedKey[:])
	aad := hashParts(p.derived[24:], p.plain, []byte("415"), point, []byte(strconv.FormatUint(credential, 10)))
	return envelopeOpen(encrypted, sharedKey[:24], aad)
}

package wechat

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
)

type protocolKey struct {
	curve   elliptic.Curve
	private []byte
}

func generateProtocolKey(curve elliptic.Curve) (*protocolKey, error) {
	if curve != elliptic.P224() && curve != elliptic.P256() {
		return nil, errors.New("неподдерживаемая кривая")
	}
	private, _, _, err := elliptic.GenerateKey(curve, rand.Reader)
	if err != nil {
		return nil, err
	}
	return &protocolKey{curve: curve, private: private}, nil
}

func protocolKeyFromPrivate(curve elliptic.Curve, private []byte) (*protocolKey, error) {
	if curve != elliptic.P224() && curve != elliptic.P256() {
		return nil, errors.New("неподдерживаемая кривая")
	}
	n := new(big.Int).SetBytes(private)
	if n.Sign() <= 0 || n.Cmp(curve.Params().N) >= 0 {
		return nil, errors.New("неверный закрытый ключ")
	}
	width := (curve.Params().BitSize + 7) / 8
	padded := make([]byte, width)
	n.FillBytes(padded)
	return &protocolKey{curve: curve, private: padded}, nil
}

func (k *protocolKey) public() []byte {
	x, y := k.curve.ScalarBaseMult(k.private)
	return elliptic.Marshal(k.curve, x, y)
}
func (k *protocolKey) close() { clear(k.private); k.private = nil }

func (k *protocolKey) derive(point []byte) ([]byte, error) {
	if len(k.private) == 0 {
		return nil, errors.New("закрытый ключ уничтожен")
	}
	x, y := elliptic.Unmarshal(k.curve, point)
	if x == nil || y == nil {
		return nil, errors.New("неверная публичная точка")
	}
	x, _ = k.curve.ScalarMult(x, y, k.private)
	if x == nil {
		return nil, errors.New("неверный общий ключ")
	}
	out := make([]byte, (k.curve.Params().BitSize+7)/8)
	x.FillBytes(out)
	return out, nil
}

func verifyPinnedSignature(publicPEM, data, signature []byte) error {
	block, rest := pem.Decode(publicPEM)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		return errors.New("неверный открытый ключ сервера")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return err
	}
	ec, ok := key.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return errors.New("неподдерживаемый ключ подписи")
	}
	digest := sha256.Sum256(data)
	if !ecdsa.VerifyASN1(ec, digest[:], signature) {
		return errors.New("подпись сервера не прошла проверку")
	}
	return nil
}

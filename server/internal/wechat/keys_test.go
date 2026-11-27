package wechat

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestProtocolECDH(t *testing.T) {
	for _, curve := range []elliptic.Curve{elliptic.P224(), elliptic.P256()} {
		a, e := protocolKeyFromPrivate(curve, []byte{1})
		if e != nil {
			t.Fatal(e)
		}
		b, e := protocolKeyFromPrivate(curve, []byte{2})
		if e != nil {
			t.Fatal(e)
		}
		ab, e := a.derive(b.public())
		if e != nil {
			t.Fatal(e)
		}
		ba, e := b.derive(a.public())
		if e != nil || !bytes.Equal(ab, ba) {
			t.Fatalf("ECDH: %v", e)
		}
		if _, e = a.derive([]byte{1, 2}); e == nil {
			t.Fatal("invalid point accepted")
		}
		a.close()
		if _, e = a.derive(b.public()); e == nil {
			t.Fatal("closed key accepted")
		}
	}
	a, _ := protocolKeyFromPrivate(elliptic.P256(), []byte{1})
	b, _ := protocolKeyFromPrivate(elliptic.P256(), []byte{1, 123})
	got, e := a.derive(b.public())
	if e != nil || !bytes.Equal(got, unhex(t, "005543894af3d00ed7d740abdbd75c96b06877b787db5f70eea78b90a8d7c00a")) {
		t.Fatalf("leading zero: %x %v", got, e)
	}
	if _, e := protocolKeyFromPrivate(elliptic.P256(), []byte{0}); e == nil {
		t.Fatal("zero scalar accepted")
	}
}

func TestPinnedSignature(t *testing.T) {
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	der, e := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	data := []byte("synthetic handshake")
	hash := sha256.Sum256(data)
	sig, e := ecdsa.SignASN1(rand.Reader, k, hash[:])
	if e != nil {
		t.Fatal(e)
	}
	if e = verifyPinnedSignature(pub, data, sig); e != nil {
		t.Fatal(e)
	}
	sig[len(sig)-1] ^= 1
	if e = verifyPinnedSignature(pub, data, sig); e == nil {
		t.Fatal("invalid signature accepted")
	}
}

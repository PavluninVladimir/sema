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

func TestNoticeVectors(t *testing.T) {
	vectors := map[string]uint32{"": 1661159480, "a": 1665223448, "abc": 1668286726, "synthetic notice checksum": 1662085121}
	for input, want := range vectors {
		if got := noticeID([]byte(input)); got != want {
			t.Errorf("notice(%q)=%d want %d", input, got, want)
		}
	}
}

func TestPreloginAuthenticatesServer(t *testing.T) {
	server, err := generateProtocolKey(elliptic.P256())
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&signer.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pin := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	p, err := preparePrelogin([]byte("request"), businessProfile{point: server.public(), signaturePEM: pin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	shared, err := server.derive(p.local.public())
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256(shared)
	point := server.public()
	aad := hashParts(p.derived[24:], []byte("request415"), point, []byte("1"))
	encrypted, err := sealRandomEnvelope([]byte("reply"), key[:24], aad)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := ecdsa.SignASN1(rand.Reader, signer, hashParts(hashParts(p.encrypted, encrypted)))
	if err != nil {
		t.Fatal(err)
	}
	pointFields, _ := appendUint(nil, 1, 415)
	pointFields, _ = appendBytes(pointFields, 2, point)
	reply, _ := appendBytes(nil, 1, pointFields)
	reply, _ = appendUint(reply, 2, 1)
	reply, _ = appendBytes(reply, 3, encrypted)
	reply, _ = appendBytes(reply, 4, signature)
	got, err := p.decryptReply(reply)
	if err != nil || !bytes.Equal(got, []byte("reply")) {
		t.Fatalf("reply: %q %v", got, err)
	}
	reply[len(reply)-1] ^= 1
	if _, err = p.decryptReply(reply); err == nil {
		t.Fatal("invalid signature accepted")
	}
}

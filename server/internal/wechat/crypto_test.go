package wechat

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"testing"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, e := hex.DecodeString(s)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestHKDFExpandRFC5869(t *testing.T) {
	key := unhex(t, "077709362c2e32df0ddc3f0dc47bba6390b6c73bb50f9c3122ec844ad7c2b3e5")
	info := unhex(t, "f0f1f2f3f4f5f6f7f8f9")
	want := unhex(t, "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865")
	got, e := hkdfExpand(key, info, 42)
	if e != nil || !bytes.Equal(got, want) {
		t.Fatalf("HKDF: %x %v", got, e)
	}
	for _, n := range []int{0, -1, 8161} {
		if _, e := hkdfExpand(key, info, n); e == nil {
			t.Fatalf("accepted %d", n)
		}
	}
}

func TestSessionKnownAnswerAndTampering(t *testing.T) {
	fixture := unhex(t, "9e65093555920046fdfa90d437e2f0b922d46623768c7870b390381087bb0d11fa5af671c9b301db000102030405060708090a0b0d4ecbc764b639df9549f03800165ea6")
	key := make([]byte, 24)
	for i := range key {
		key[i] = byte(i)
	}
	plain, e := envelopeOpen(fixture, key, nil)
	if e != nil || string(plain) != "synthetic native session request" {
		t.Fatalf("fixture: %q %v", plain, e)
	}
	for _, i := range []int{0, len(fixture) - 28, len(fixture) - 1} {
		bad := bytes.Clone(fixture)
		bad[i] ^= 1
		if _, e := envelopeOpen(bad, key, nil); e == nil {
			t.Fatalf("accepted alteration %d", i)
		}
	}
	nonce := make([]byte, 12)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	sealed, e := envelopeSeal(plain, key, nil, nonce)
	if e != nil {
		t.Fatal(e)
	}
	result, e := envelopeOpen(sealed, key, nil)
	if e != nil || !bytes.Equal(result, plain) {
		t.Fatalf("round trip: %v", e)
	}
}

func TestCompressionBounds(t *testing.T) {
	compressed, e := deflate([]byte("sample"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = inflate(append(compressed, 1)); e == nil {
		t.Fatal("trailing bytes accepted")
	}
	var zip bytes.Buffer
	z := zlib.NewWriter(&zip)
	_, e = z.Write(make([]byte, maxMessageBytes+1))
	if e != nil {
		t.Fatal(e)
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = inflate(zip.Bytes()); e == nil {
		t.Fatal("oversized decompression accepted")
	}
}

func TestLegacyCBC(t *testing.T) {
	for _, key := range [][]byte{[]byte("key"), bytes.Repeat([]byte{4}, 16), bytes.Repeat([]byte{3}, 32)} {
		for _, size := range []int{0, 1, 16, 33} {
			plain := bytes.Repeat([]byte{7}, size)
			enc, e := legacyCBC(plain, key, false)
			if e != nil {
				t.Fatal(e)
			}
			got, e := legacyCBC(enc, key, true)
			if e != nil || !bytes.Equal(got, plain) {
				t.Fatalf("CBC %d: %v", size, e)
			}
		}
	}
	if _, e := legacyCBC([]byte{1}, []byte("key"), true); e == nil {
		t.Fatal("truncated CBC accepted")
	}
	if _, e := legacyCBC(nil, nil, false); e == nil {
		t.Fatal("empty key accepted")
	}
}

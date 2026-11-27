package wechat

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"io"
)

// hkdfExpand implements the protocol's RFC 5869 expand phase.
func hkdfExpand(secret, info []byte, size int) ([]byte, error) {
	if size <= 0 || size > 255*sha256.Size {
		return nil, errors.New("неверная длина HKDF")
	}
	out := make([]byte, 0, size+sha256.Size)
	var last []byte
	for i := 1; len(out) < size; i++ {
		h := hmac.New(sha256.New, secret)
		h.Write(last)
		h.Write(info)
		h.Write([]byte{byte(i)})
		last = h.Sum(nil)
		out = append(out, last...)
	}
	clear(last)
	return out[:size], nil
}

func gcmCipher(key, nonce []byte) (cipher.AEAD, error) {
	if len(key) != 16 && len(key) != 24 {
		return nil, errors.New("неверная длина ключа GCM")
	}
	if len(nonce) != 12 {
		return nil, errors.New("неверная длина nonce GCM")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func gcmSeal(key, nonce, aad, plain []byte) ([]byte, error) {
	g, err := gcmCipher(key, nonce)
	if err != nil {
		return nil, err
	}
	return g.Seal(nil, nonce, plain, aad), nil
}

func gcmOpen(key, nonce, aad, sealed []byte) ([]byte, error) {
	g, err := gcmCipher(key, nonce)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.Overhead() {
		return nil, errors.New("обрезанная запись GCM")
	}
	return g.Open(nil, nonce, sealed, aad)
}

func deflate(plain []byte) ([]byte, error) {
	if len(plain) > maxMessageBytes {
		return nil, errors.New("сообщение слишком велико")
	}
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	if _, err := z.Write(plain); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func inflate(input []byte) ([]byte, error) {
	source := bytes.NewReader(input)
	z, err := zlib.NewReader(source)
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(z, maxMessageBytes+1))
	if err != nil {
		z.Close()
		return nil, err
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	if len(out) > maxMessageBytes {
		return nil, errors.New("распакованное сообщение слишком велико")
	}
	if source.Len() != 0 {
		return nil, errors.New("лишние данные после сжатого сообщения")
	}
	return out, nil
}

func envelopeSeal(plain, key, aad, nonce []byte) ([]byte, error) {
	if len(key) != 24 {
		return nil, errors.New("требуется ключ AES-192")
	}
	compressed, err := deflate(plain)
	if err != nil {
		return nil, err
	}
	encrypted, err := gcmSeal(key, nonce, aad, compressed)
	if err != nil {
		return nil, err
	}
	n := len(encrypted) - 16
	out := make([]byte, 0, len(encrypted)+len(nonce))
	out = append(out, encrypted[:n]...)
	out = append(out, nonce...)
	out = append(out, encrypted[n:]...)
	return out, nil
}

func envelopeOpen(input, key, aad []byte) ([]byte, error) {
	if len(key) != 24 || len(input) < 29 || len(input) > maxMessageBytes+65536 {
		return nil, errors.New("неверный зашифрованный пакет")
	}
	n := len(input)
	nonce := input[n-28 : n-16]
	sealed := make([]byte, 0, n-12)
	sealed = append(sealed, input[:n-28]...)
	sealed = append(sealed, input[n-16:]...)
	zipped, err := gcmOpen(key, nonce, aad, sealed)
	if err != nil {
		return nil, err
	}
	defer clear(zipped)
	return inflate(zipped)
}

// legacyCBC implements the key and IV normalization used by this protocol.
func legacyCBC(input, key []byte, decrypt bool) ([]byte, error) {
	if len(key) == 0 || len(key) > 65536 || len(input) > maxMessageBytes+65536 {
		return nil, errors.New("неверный ключ или размер CBC")
	}
	var normalized [16]byte
	copy(normalized[:], key)
	defer clear(normalized[:])
	block, err := aes.NewCipher(normalized[:])
	if err != nil {
		return nil, err
	}
	if decrypt {
		if len(input) == 0 || len(input)%aes.BlockSize != 0 {
			return nil, errors.New("неверная длина CBC")
		}
		out := make([]byte, len(input))
		cipher.NewCBCDecrypter(block, normalized[:]).CryptBlocks(out, input)
		padding := int(out[len(out)-1])
		if padding == 0 || padding > aes.BlockSize || padding > len(out) {
			clear(out)
			return nil, errors.New("неверное дополнение CBC")
		}
		var bad byte
		for _, v := range out[len(out)-padding:] {
			bad |= v ^ byte(padding)
		}
		if bad != 0 {
			clear(out)
			return nil, errors.New("неверное дополнение CBC")
		}
		return out[:len(out)-padding], nil
	}
	padding := aes.BlockSize - len(input)%aes.BlockSize
	out := make([]byte, len(input)+padding)
	copy(out, input)
	for i := len(input); i < len(out); i++ {
		out[i] = byte(padding)
	}
	cipher.NewCBCEncrypter(block, normalized[:]).CryptBlocks(out, out)
	return out, nil
}

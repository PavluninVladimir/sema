// Package secret — шифрование секретов Сёмы: ключей моделей, паролей ящиков,
// секрета OAuth и токена обновления. Ключ берётся из переменной окружения и
// никогда не попадает ни в базу, ни в каталог данных, поэтому утёкший файл
// базы или резервная копия не дают доступа ни к почте, ни к моделям.
//
// От того, у кого есть права на самой машине, это не спасает: ключ доступен
// процессу. Так и написано в настройках — обещать больше было бы нечестно.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// prefix помечает зашифрованное значение: по нему отличаем уже закрытые
// записи от старых открытых и сможем сменить схему, не ломая прежние.
const prefix = "enc:v1:"

type Box struct {
	aead cipher.AEAD
}

// New делает шифровальщик из ключа любой длины: ключ приводится к 32 байтам
// хешем, чтобы человеку не приходилось подбирать длину.
func New(key string) (*Box, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil // ключа нет — работаем без шифрования
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypted — похоже ли значение на уже зашифрованное.
func Encrypted(v string) bool { return strings.HasPrefix(v, prefix) }

// Seal шифрует значение. Пустое остаётся пустым: нечего прятать.
func (b *Box) Seal(v string) (string, error) {
	if b == nil || v == "" || Encrypted(v) {
		return v, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(v), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open расшифровывает значение. Незашифрованное возвращается как есть —
// так работающая установка переживает появление ключа.
func (b *Box) Open(v string) (string, error) {
	if !Encrypted(v) {
		return v, nil
	}
	if b == nil {
		return "", fmt.Errorf("значение зашифровано, а ключ шифрования не задан")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, prefix))
	if err != nil {
		return "", fmt.Errorf("испорченное зашифрованное значение: %w", err)
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", fmt.Errorf("испорченное зашифрованное значение")
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", fmt.Errorf("ключ шифрования не подходит к сохранённым секретам")
	}
	return string(plain), nil
}

package secret

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// Фраза восстановления — единственная резервная копия ключа шифрования.
// Формат: 24 знака из алфавита без похожих (нет 0/O, 1/l/I), группами по
// четыре через дефис, последняя группа — контрольная сумма. Опечатка в
// фразе ловится суммой и отличается от неверной фразы: «вы ошиблись в
// одном знаке» — не то же самое, что «это не та фраза».
const phraseAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

const phraseBody = 24 // знаков без контрольной суммы

// NewPhrase — новая фраза из случайных знаков.
func NewPhrase() (string, error) {
	b := make([]byte, phraseBody)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, x := range b {
		sb.WriteByte(phraseAlphabet[int(x)%len(phraseAlphabet)])
	}
	body := sb.String()
	return format(body + checksum(body)), nil
}

// checksum — четыре знака, зависящие от всех предыдущих и их позиций.
func checksum(body string) string {
	var acc [4]int
	for i, c := range body {
		v := strings.IndexRune(phraseAlphabet, c)
		acc[i%4] = (acc[i%4]*7 + v + i + 1) % len(phraseAlphabet)
	}
	out := make([]byte, 4)
	for i := range out {
		out[i] = phraseAlphabet[acc[i]]
	}
	return string(out)
}

func format(s string) string {
	var parts []string
	for i := 0; i < len(s); i += 4 {
		parts = append(parts, s[i:i+4])
	}
	return strings.Join(parts, "-")
}

// LooksLikePhrase — строка похожа на фразу восстановления (а не на
// произвольный ключ из окружения): только наш алфавит и та же длина.
func LooksLikePhrase(s string) bool {
	n := normalize(s)
	if len(n) != phraseBody+4 {
		return false
	}
	for _, c := range n {
		if !strings.ContainsRune(phraseAlphabet, c) {
			return false
		}
	}
	return true
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "", " ", "", " ", "").Replace(s)
	return s
}

// ParsePhrase — фраза в ключ: регистр, пробелы и дефисы не важны;
// неверная контрольная сумма — «опечатка», не «неверная фраза».
func ParsePhrase(s string) (string, error) {
	n := normalize(s)
	if !LooksLikePhrase(s) {
		return "", fmt.Errorf("фраза должна быть из %d знаков алфавита «%s» группами по четыре", phraseBody+4, phraseAlphabet)
	}
	body, sum := n[:phraseBody], n[phraseBody:]
	if checksum(body) != sum {
		return "", fmt.Errorf("в фразе опечатка: контрольная группа не сходится")
	}
	return format(n), nil
}

// NewFromEnv — ключ из окружения: фраза восстановления приводится к канону
// (регистр, пробелы, дефисы не важны, как и обещано), всё остальное —
// как есть.
func NewFromEnv(raw string) (*Box, error) {
	if LooksLikePhrase(raw) {
		if canon, err := ParsePhrase(raw); err == nil {
			return New(canon)
		}
	}
	return New(raw)
}

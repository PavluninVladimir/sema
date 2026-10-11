package ca

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

// Свой центр: корень подписывает агента, чужой сертификат не проходит.
func TestRootSignsAgent(t *testing.T) {
	root, err := NewRoot("Сёма", 10)
	if err != nil {
		t.Fatalf("корень: %v", err)
	}
	if NameOf(root.CertPEM) != "Сёма" {
		t.Errorf("имя корня: %q", NameOf(root.CertPEM))
	}
	if fp := Fingerprint(root.CertPEM); len(strings.Split(fp, ":")) != 8 {
		t.Errorf("отпечаток нечитаем: %q", fp)
	}

	keyPEM, pubDER, err := NewKey()
	if err != nil {
		t.Fatalf("ключ агента: %v", err)
	}
	certPEM, err := Sign(root, "мак", pubDER, 365)
	if err != nil {
		t.Fatalf("подпись: %v", err)
	}
	if NameOf(certPEM) != "мак" {
		t.Errorf("имя агента в сертификате: %q", NameOf(certPEM))
	}
	until, err := Until(certPEM)
	if err != nil || until.Before(time.Now().AddDate(0, 0, 360)) {
		t.Errorf("срок годности: %v, %v", until, err)
	}
	// пара сходится: ключ подходит к сертификату
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		t.Fatalf("ключ не подходит к сертификату: %v", err)
	}
	// корень признаёт своего
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(root.CertPEM)) {
		t.Fatal("корень не читается как доверенный")
	}
	if err := verify(certPEM, pool); err != nil {
		t.Errorf("свой сертификат не признан: %v", err)
	}
}

// Сертификат чужого центра не должен признаваться своим.
func TestForeignCertRejected(t *testing.T) {
	mine, _ := NewRoot("Сёма", 10)
	foreign, _ := NewRoot("Чужой", 10)
	_, pub, _ := NewKey()
	certPEM, err := Sign(foreign, "самозванец", pub, 30)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(mine.CertPEM))
	if err := verify(certPEM, pool); err == nil {
		t.Fatal("чужой сертификат признан своим")
	}
}

// Подпись возможна только ключом Ed25519 и только разобранным ключом.
func TestSignRejectsGarbage(t *testing.T) {
	root, _ := NewRoot("Сёма", 10)
	if _, err := Sign(root, "мак", []byte("не ключ"), 30); err == nil {
		t.Error("подписали мусор вместо ключа")
	}
	if _, err := Sign(Pair{}, "мак", nil, 30); err == nil {
		t.Error("подписали пустым корнем")
	}
}

func verify(certPEM string, pool *x509.CertPool) error {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return errNotCert
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err
}

var errNotCert = errors.New("это не сертификат")

// Сертификат перевыпускался только по сроку: сменишь имя сервера в
// настройке — агенты молча остаются без связи, и выглядит это как их
// поломка. Значит имена сертификата надо уметь прочитать и сравнить.
func TestCertificateNames(t *testing.T) {
	root, err := NewRoot("Сёма", 10)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := NewServer(root, "sema", []string{"sema", "localhost", "127.0.0.1", "sema.local"}, 365)
	if err != nil {
		t.Fatal(err)
	}
	hosts := HostsOf(pair.CertPEM)
	have := map[string]bool{}
	for _, h := range hosts {
		have[h] = true
	}
	for _, want := range []string{"sema", "localhost", "sema.local", "127.0.0.1"} {
		if !have[want] {
			t.Errorf("в сертификате нет %q (есть: %v)", want, hosts)
		}
	}
	if HostsOf("не сертификат") != nil {
		t.Fatal("мусор разобрался как сертификат")
	}
}

package ca

// Свой удостоверяющий центр: Сёма выдаёт сертификаты своим агентам сама.
// Покупать чужие и заводить внешние службы незачем — всё должно работать
// в домашней сети без интернета и без чужого доверия.
//
// Только стандартная библиотека и ключи Ed25519: короткие, быстрые, без
// выбора параметров, которые можно выбрать неправильно.

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

// Pair — сертификат и ключ в PEM: так их удобно хранить в секретах.
type Pair struct {
	CertPEM string
	KeyPEM  string
}

// NewRoot — корень Сёмы. Живёт долго: перевыпуск означает переподписать
// всех агентов, и делать это по расписанию незачем.
func NewRoot(name string, years int) (Pair, error) {
	if years <= 0 {
		years = 10
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Pair{}, err
	}
	serial, err := serialNumber()
	if err != nil {
		return Pair{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name, Organization: []string{"Сёма"}},
		NotBefore:             time.Now().Add(-time.Hour), // часы на разных машинах расходятся
		NotAfter:              time.Now().AddDate(years, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, priv)
	if err != nil {
		return Pair{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return Pair{}, err
	}
	return Pair{
		CertPEM: encode("CERTIFICATE", der),
		KeyPEM:  encode("PRIVATE KEY", keyDER),
	}, nil
}

// NewKey — ключевая пара агента. Приватная часть остаётся у агента и
// Сёме не показывается: она подписывает открытый ключ, не зная закрытого.
func NewKey() (privPEM string, pubDER []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", nil, err
	}
	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", nil, err
	}
	return encode("PRIVATE KEY", keyDER), pubBytes, nil
}

// Sign подписывает открытый ключ агента корнем Сёмы. Имя агента ложится в
// сертификат: по нему Сёма потом узнаёт, кто звонит.
func Sign(root Pair, agentName string, pubDER []byte, days int) (string, error) {
	if days <= 0 {
		days = 365
	}
	rootCert, rootKey, err := parsePair(root)
	if err != nil {
		return "", err
	}
	pub, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		return "", fmt.Errorf("открытый ключ агента не разобран: %w", err)
	}
	if _, ok := pub.(ed25519.PublicKey); !ok {
		return "", fmt.Errorf("нужен ключ Ed25519")
	}
	serial, err := serialNumber()
	if err != nil {
		return "", err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: agentName, Organization: []string{"Сёма-агент"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 0, days),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"sema-agent"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, rootCert, pub, rootKey)
	if err != nil {
		return "", err
	}
	return encode("CERTIFICATE", der), nil
}

// NewServer — сертификат самой Сёмы для приёма агентов. Корень для этого
// не годится: у него нет имён, и агент справедливо откажется его
// признавать сервером.
func NewServer(root Pair, name string, hosts []string, days int) (Pair, error) {
	if days <= 0 {
		days = 365
	}
	rootCert, rootKey, err := parsePair(root)
	if err != nil {
		return Pair{}, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Pair{}, err
	}
	serial, err := serialNumber()
	if err != nil {
		return Pair{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name, Organization: []string{"Сёма"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 0, days),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
			continue
		}
		tpl.DNSNames = append(tpl.DNSNames, h)
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, rootCert, pub, rootKey)
	if err != nil {
		return Pair{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return Pair{}, err
	}
	return Pair{CertPEM: encode("CERTIFICATE", der), KeyPEM: encode("PRIVATE KEY", keyDER)}, nil
}

// Fingerprint — отпечаток сертификата в том же виде, что у серверов SSH:
// человек сверяет его глазами, значит он должен читаться.
func Fingerprint(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	hexed := hex.EncodeToString(sum[:])
	var parts []string
	for i := 0; i+4 <= len(hexed) && len(parts) < 8; i += 4 {
		parts = append(parts, hexed[i:i+4])
	}
	return strings.Join(parts, ":")
}

// NameOf — чьё имя стоит в сертификате.
func NameOf(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return cert.Subject.CommonName
}

// Since — с какого момента годен сертификат: когда он выдан.
func Since(certPEM string) (time.Time, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return time.Time{}, fmt.Errorf("это не сертификат")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotBefore, nil
}

// Until — до какого срока годен сертификат.
func Until(certPEM string) (time.Time, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return time.Time{}, fmt.Errorf("это не сертификат")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}

func parsePair(p Pair) (*x509.Certificate, crypto.Signer, error) {
	cb, _ := pem.Decode([]byte(p.CertPEM))
	kb, _ := pem.Decode([]byte(p.KeyPEM))
	if cb == nil || kb == nil {
		return nil, nil, fmt.Errorf("корень не разобран")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("ключ корня не умеет подписывать")
	}
	return cert, signer, nil
}

func serialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func encode(kind string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
}

// HostsOf — имена и адреса, на которые выписан сертификат. Нужны, чтобы
// заметить расхождение с настройкой: сертификат перевыпускался только по
// сроку, и смена имён в настройке молча оставляла агентов без связи.
func HostsOf(certPEM string) []string {
	blk, _ := pem.Decode([]byte(certPEM))
	if blk == nil {
		return nil
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil
	}
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

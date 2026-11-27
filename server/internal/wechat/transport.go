package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

func dialMMTLS(ctx context.Context, host string) (*mmtlsSession, error) {
	if !validWeChatHost(host) {
		return nil, errors.New("недопустимый сервер WeChat")
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "80"))
	if err != nil {
		return nil, err
	}
	s, err := authenticateMMTLS(ctx, conn, serverPins)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// The request body and response are protected by MMTLS. The outer HTTP
// connection never follows redirects or uses a proxy from the environment.
var shortHTTP = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 32 * 1024,
	},
}

func (s *mmtlsSession) exchangeShort(ctx context.Context, host, path string, payload []byte) ([]byte, error) {
	hello, extensions, wire, err := prepareShortExchange(s, host, path, payload, uint32(time.Now().Unix()))
	if err != nil {
		return nil, err
	}
	var token [4]byte
	if _, err = rand.Read(token[:]); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+host+"/mmtls/"+hex.EncodeToString(token[:]), bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.GetBody = nil
	req.Close = true
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "MicroMessenger Client")
	req.Header.Set("Upgrade", "mmtls")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := shortHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("shortlink: HTTP %d", resp.StatusCode)
	}
	result, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes+1))
	if err != nil {
		return nil, err
	}
	return decodeShortReply(result, s.accessKey, hello, extensions)
}

// Package google — OAuth 2.0 для Gmail (loopback-flow для desktop-приложений).
// Одноразовый вход: sema -google-login открывает браузер, refresh-токен
// сохраняется в SQLite; дальше сервер сам обновляет access-токены.
package google

import (
	"github.com/PavluninVladimir/sema/server/internal/host"

	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	authEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenEndpoint = "https://oauth2.googleapis.com/token"
	scope         = "https://mail.google.com/" // полный IMAP-доступ (нужен для move/expunge)
)

type TokenStore interface {
	Setting(key, def string) string
	SetSetting(key, value string) error
}

const refreshTokenKey = "google.refresh_token"

type OAuth struct {
	ClientID     string
	ClientSecret string
	Store        TokenStore

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// AccessToken возвращает живой access-токен, обновляя его по refresh-токену.
func (o *OAuth) AccessToken(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.token != "" && time.Now().Before(o.expiry.Add(-2*time.Minute)) {
		return o.token, nil
	}
	refresh := o.Store.Setting(refreshTokenKey, "")
	if refresh == "" {
		return "", fmt.Errorf("нет токена Google — выполните: go run ./cmd/sema -google-login")
	}
	tok, err := exchange(ctx, url.Values{
		"client_id":     {o.ClientID},
		"client_secret": {o.ClientSecret},
		"refresh_token": {refresh},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return "", fmt.Errorf("refresh google token: %w", err)
	}
	o.token = tok.AccessToken
	o.expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return o.token, nil
}

// Login проводит интерактивный вход: браузер → согласие → код на
// loopback-адрес → refresh-токен в Store.
func (o *OAuth) Login(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://%s", ln.Addr().String())
	state := rand.Text()
	verifier := rand.Text() + rand.Text()
	challenge := sha256.Sum256([]byte(verifier))

	authURL := authEndpoint + "?" + url.Values{
		"client_id":             {o.ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {scope},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}.Encode()

	fmt.Println("Открываю браузер для входа в Google…")
	fmt.Println("Если не открылся, перейдите вручную:\n\n  " + authURL + "\n")
	openBrowser(authURL)

	codeCh := make(chan string, 1)
	srv := &http.Server{Handler: loginCallback(state, codeCh), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	var code string
	select {
	case code = <-codeCh:
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("вход не завершён за 5 минут")
	case <-ctx.Done():
		return ctx.Err()
	}

	tok, err := exchange(ctx, url.Values{
		"client_id":     {o.ClientID},
		"client_secret": {o.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirect},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	})
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return fmt.Errorf("Google не вернул refresh_token (отзовите доступ на myaccount.google.com/permissions и попробуйте снова)")
	}
	return o.Store.SetSetting(refreshTokenKey, tok.RefreshToken)
}

func loginCallback(state string, codeCh chan<- string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet || r.URL.Path != "/" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "неверное подтверждение входа", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "нет кода входа", http.StatusBadRequest)
			return
		}
		select {
		case codeCh <- code:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<h2>Код получен, проверяем вход в Gmail</h2>Можно вернуться в терминал.")
		default:
			http.Error(w, "код уже получен", http.StatusConflict)
		}
	})
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// Свой транспорт для запросов к Google:
//   - таймаут (зависший запрос держал мьютекс токена и блокировал весь IMAP);
//   - IPv4 + резолв через 8.8.8.8 (роутер может не отдавать A-запись,
//     а IPv6-маршрута нет) с фолбэком на системный DNS.
var oauthResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, "udp", "8.8.8.8:53")
	},
}

var oauthHTTP = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := &net.Dialer{Timeout: 10 * time.Second, Resolver: oauthResolver}
			if conn, err := d.DialContext(ctx, "tcp4", addr); err == nil {
				return conn, nil
			}
			sys := &net.Dialer{Timeout: 10 * time.Second}
			return sys.DialContext(ctx, network, addr)
		},
	},
}

func exchange(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw)
	}
	tok := &tokenResponse{}
	if err := json.Unmarshal(raw, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

func openBrowser(u string) { host.OpenInBrowser(u) }

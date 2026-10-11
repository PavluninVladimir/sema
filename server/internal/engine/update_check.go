package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/version"
)

// Проверка новой версии. Выключена по умолчанию: Сёма ничего не сообщает
// наружу, пока человек не включит задачу и не задаст адрес. Наружу уходит
// ровно одно — нынешняя версия сборки; ответ ничего не меняет сам, а
// приходит карточкой в «Уведомления». Запрос виден на «Что ушло».
//
// Что это защищает и чего нет: суммы sha256 у сборок и отпечатки образов
// ловят порчу при скачивании и подмену файла на диске — но не подмену на
// самом источнике: если реестр или сайт взломан, суммы будут «верными».
// Это сказано и на экране.
const (
	WorkerUpdate  = "update"
	updateURLKey  = "update.url"
	updateTimeout = 10 * time.Second
)

// updateSchedule — раз в сутки со сдвигом: у каждой машины своя минута,
// чтобы все Сёмы мира не стучались в один момент.
func updateSchedule() string {
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte(host))
	h := 3 + int(sum[0])%3 // 03–05 утра по местному времени
	m := int(sum[1]) % 60
	return fmt.Sprintf("каждый день в %02d:%02d", h, m)
}

// updateAllowLocal — только для тестов: разрешить адрес в своей сети.
var updateAllowLocal = false

// updateHostAllowed — адрес проверки должен вести наружу: запрос в свою
// сеть от имени Сёмы (SSRF) здесь не нужен никому. Проверяется по адресам,
// в которые разрешается имя, а не по строке.
// ownNetwork — адрес свой: loopback, частные сети, link-local, CGNAT
// (100.64/10 — Tailscale), NAT64 (64:ff9b::/96), 0.0.0.0/8.
func ownNetwork(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, cidr := range []string{"100.64.0.0/10", "64:ff9b::/96", "0.0.0.0/8", "192.0.0.0/24", "198.18.0.0/15"} {
		if _, n, err := net.ParseCIDR(cidr); err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// updateDial — соединение по проверенному адресу: имя разрешается здесь
// же, и звоним ровно на тот IP, который проверили — между проверкой и
// запросом DNS не успевает подменить ответ.
func updateDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("имя %s не разрешается: %v", host, err)
	}
	var d net.Dialer
	for _, ip := range ips {
		if !updateAllowLocal && ownNetwork(ip.IP) {
			return nil, fmt.Errorf("адрес проверки ведёт в свою сеть (%s) — проверка версии только снаружи", ip.IP)
		}
	}
	for _, ip := range ips {
		if c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port)); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("не удалось соединиться с %s", host)
}

// UpdateURL — адрес проверки; пусто — проверка не идёт.
func (e *Engine) UpdateURL() string { return strings.TrimSpace(e.store.Setting(updateURLKey, "")) }

// SetUpdateURL — задать адрес; принимаются только http(s).
func (e *Engine) SetUpdateURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		u, err := url.Parse(raw)
		// только https: ответ ничем не подписан, и без шифрования любой в
		// сети подсунул бы карточку «новая версия» с любой ссылкой
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("адрес проверки должен быть вида https://…")
		}
	}
	return e.store.SetSetting(updateURLKey, raw)
}

// updateReply — что отвечает источник: JSON с версией, либо просто текст.
type updateReply struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
	URL     string `json:"url"`
}

// updatePass — одна проверка. Неудача ничего не ломает: строка результата
// у задачи, и всё.
func (e *Engine) updatePass(ctx context.Context) string {
	target := e.UpdateURL()
	if target == "" {
		return "адрес проверки не задан — задайте на «Здоровье» или выключите задачу"
	}
	u, err := url.Parse(target)
	if err != nil {
		return "адрес не разбирается: " + err.Error()
	}
	q := u.Query()
	q.Set("v", version.String())
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err.Error()
	}
	req.Header.Set("User-Agent", "Sema/"+version.String())
	// переадресации не ходим: 302 увёл бы запрос на любой адрес, в том
	// числе в свою сеть; адрес проверяется в момент соединения
	client := &http.Client{Timeout: updateTimeout,
		Transport:     &http.Transport{DialContext: updateDial, TLSHandshakeTimeout: updateTimeout},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	started := time.Now()
	// в журнал — запрос целиком, как ушёл: человек задал адрес с параметрами,
	// и «видно на Что ушло» должно значить видно
	row := &store.SentRow{Consumer: "core:update", Model: u.Host, Outside: true, Kind: "http",
		Class: store.ClassPlain, Preview: "GET " + u.String()}
	resp, err := client.Do(req)
	if err != nil {
		row.OK, row.Err, row.Seconds = false, err.Error(), time.Since(started).Seconds()
		_ = e.store.AddSent(row)
		return "источник не ответил: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	row.OK, row.Seconds = resp.StatusCode < 300, time.Since(started).Seconds()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		row.OK, row.Err = false, "переадресация "+resp.Status+" — не хожу"
	} else if !row.OK {
		row.Err = resp.Status
	}
	_ = e.store.AddSent(row)
	if !row.OK {
		return "источник: " + row.Err
	}
	var r updateReply
	if json.Unmarshal(body, &r) != nil || r.Version == "" {
		r.Version = strings.TrimSpace(string(body))
	}
	r.Version = strings.Map(func(c rune) rune {
		if c < 32 || c == 127 {
			return -1
		}
		return c
	}, strings.TrimSpace(r.Version))
	if r.Version == "" || len([]rune(r.Version)) > 64 {
		return "источник не назвал версию"
	}
	if r.URL != "" && (!strings.HasPrefix(r.URL, "https://") || len([]rune(r.URL)) > 300) {
		r.URL = "" // ссылку в карточку — только https и короткую
	}
	if r.Version == version.String() {
		return "новых версий нет · " + version.String()
	}
	summary := "У вас " + version.String() + ", доступна " + r.Version + ". Перед обновлением снимите копию (sema -backup) и сверьте sha256 сборки или отпечаток образа: суммы ловят порчу и подмену файла, но не подмену на самом источнике."
	if r.Notes != "" {
		summary += " " + clip(r.Notes, 300)
	}
	if r.URL != "" {
		summary += " " + r.URL
	}
	summary = clip(summary, 700)
	_, _ = e.Notify(&store.Notification{
		Key: "update:" + r.Version, Source: "core", Level: "info",
		Title: "Есть новая версия Сёмы: " + r.Version, Summary: summary,
	})
	return "доступна " + r.Version + " · у вас " + version.String()
}

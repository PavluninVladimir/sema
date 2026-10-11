package main

// semacheck perf — повторяемый замер экранов работающей Сёмы (perf-pass 8).
//
// Тот же обход, что разовый скрипт 3 октября 2026: от меню и разделов по
// ссылкам `/ui?screen=` и `data-goto`, по несколько экземпляров каждого вида
// (сущности и номера: `script:N`, `memory:node:N`, `settings:servers@*`),
// три запроса на экран.
//
// Замер не должен менять живую Сёму: чат ставит «прочитано», «Почта»
// сдвигает окно дайджеста, экран агента спрашивает жителя машины и пишет его
// журнал. Поэтому экран просится так же, как его подогревает клиент при
// наведении (`X-Sema-Prefetch`): сервер сам решает, что можно (список
// `data-prefetch`), собирает такой экран, ничего не делая наружу, а экрану,
// которому без сети или агента показать нечего, отвечает 204 — он считается
// пропущенным. Для остальных экранов такая сборка и есть обычная: пропускать
// ей было нечего. След замер всё же оставляет — строку журнала доступа на
// каждый запрос по токену («Вход и доступ → Кто стучался»): так задумано.
//
// Только GET с токеном из SEMA_TOKEN; токен не печатается и в JSON не
// пишется. Итог — таблица в вывод и JSON рядом с прошлыми прогонами, с
// разницей «было → стало» по каждому экрану.
//
//	SEMA_TOKEN=… semacheck perf [-base https://sema.local:8443] [-dir ~/.sema-perf]
//	                            [-prev прошлый.json] [-phone] [-cap 3] [-max 400]
//
// На Pi мерить с самого Pi (`-base http://127.0.0.1:8765`): с мака в цифры
// попадает Wi-Fi до Pi, а он и есть главный шум. Там же видна нагрузка
// машины до и после прогона: локальная модель за работой удваивает время
// любого экрана, и такое «замедление» — не про код.

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// perfScreen — один экран прогона.
type perfScreen struct {
	Screen   string             `json:"screen"`
	Kind     string             `json:"kind"`
	Status   int                `json:"status"`   // худший ответ из всех запросов к экрану
	FirstMs  float64            `json:"first_ms"` // первое открытие: холодные кэши
	TotalMs  float64            `json:"total_ms"` // медиана повторов
	TTFBMs   float64            `json:"ttfb_ms"`  // медиана повторов до первого байта
	NetKB    float64            `json:"net_kb"`   // по сети (сжатое, если сервер сжал)
	RawKB    float64            `json:"raw_kb"`   // разметка
	Elements int                `json:"elements"` // открывающих тегов — так же, как в тестах сервера
	Timing   map[string]float64 `json:"timing"`   // Server-Timing последнего повтора, мс
	Queries  map[string]int     `json:"queries"`  // запросов к базе по частям
	Error    string             `json:"error,omitempty"`
}

// perfRun — весь прогон.
type perfRun struct {
	At      string       `json:"at"`
	Base    string       `json:"base"`
	Build   string       `json:"build"`
	Phone   bool         `json:"phone"`
	Load    [2]string    `json:"load"`              // средняя нагрузка машины до и после, если мерили на ней
	Skipped []string     `json:"skipped,omitempty"` // экраны, которым без сети показать нечего (204)
	Screens []perfScreen `json:"screens"`
}

type perfOptions struct {
	base, token, dir, prev string
	phone, verify          bool
	repeat, perKind, max   int
	pause                  time.Duration
	out                    io.Writer
}

func perfMain(args []string) {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("perf", flag.ExitOnError)
	o := perfOptions{out: os.Stdout}
	base := os.Getenv("SEMA_URL")
	if base == "" {
		base = "https://sema.local:8443"
	}
	fs.StringVar(&o.base, "base", base, "адрес Сёмы (SEMA_URL)")
	fs.StringVar(&o.dir, "dir", filepath.Join(home, ".sema-perf"), "куда класть прогоны")
	fs.StringVar(&o.prev, "prev", "", "с каким прогоном сравнивать (по умолчанию — последний в -dir)")
	fs.BoolVar(&o.phone, "phone", false, "мерить экраны телефона (X-Sema-Phone)")
	fs.BoolVar(&o.verify, "verify", false, "проверять сертификат и у домашнего адреса (.local, IP)")
	fs.IntVar(&o.repeat, "repeat", 3, "запросов на экран")
	fs.IntVar(&o.perKind, "cap", 3, "экземпляров одного вида (сущности, номера)")
	fs.IntVar(&o.max, "max", 400, "экранов не больше")
	fs.DurationVar(&o.pause, "pause", 300*time.Millisecond, "пауза между запросами: Pi занят и без замера")
	_ = fs.Parse(args)
	o.token = strings.TrimSpace(os.Getenv("SEMA_TOKEN"))
	if o.token == "" {
		log.Fatal("нужен токен в SEMA_TOKEN (Настройки → Вход и доступ → Токены, право «экраны»)")
	}
	if o.repeat < 2 {
		o.repeat = 2 // первое открытие считается отдельно, медиане нужен хотя бы один повтор
	}
	run, err := perfCrawl(o)
	if err != nil {
		log.Fatal(err)
	}
	prev := o.prev
	if prev == "" {
		prev = latestRun(o.dir, o.phone)
	}
	if err := os.MkdirAll(o.dir, 0o700); err != nil {
		log.Fatal(err)
	}
	name := filepath.Join(o.dir, "perf-"+time.Now().Format("20060102-150405")+map[bool]string{true: "-phone"}[o.phone]+".json")
	body, _ := json.MarshalIndent(run, "", " ")
	if err := os.WriteFile(name, body, 0o600); err != nil {
		log.Fatal(err)
	}
	perfSummary(o.out, run)
	if prev != "" {
		if was, err := readRun(prev); err == nil {
			perfCompare(o.out, was, run, prev)
		} else {
			fmt.Fprintf(o.out, "\nпрошлый прогон %s не читается: %v\n", prev, err)
		}
	}
	fmt.Fprintf(o.out, "\nпрогон: %s\n", name)
}

var (
	perfLinkRe     = regexp.MustCompile(`hx-get="/ui\?screen=([^"&]+)[^"]*"|data-goto="([^"]+)"`)
	perfPrefetchRe = regexp.MustCompile(`data-prefetch="([^"]*)"`)
	perfDigits     = regexp.MustCompile(`\d+`)
	perfEntity     = regexp.MustCompile(`@[^~]*`)
	// открывающий тег — тот же счёт, что у тестов сервера (markupTag)
	perfTag = regexp.MustCompile(`<[a-z][a-z0-9]*[ >/]`)
)

// perfLinks — адреса экранов из разметки: hx-get меню и вкладок, data-goto
// строк. Параметры кроме screen отрезаются: замер их не шлёт никогда.
func perfLinks(markup string) []string {
	var out []string
	for _, m := range perfLinkRe.FindAllStringSubmatch(markup, -1) {
		raw := html.UnescapeString(m[1] + m[2])
		if m[1] != "" {
			if s, err := url.QueryUnescape(raw); err == nil {
				raw = s
			}
		}
		raw, _, _ = strings.Cut(raw, "^") // метка подсветки — не адрес
		if raw == "" || strings.HasPrefix(raw, "$") {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// perfKind — вид экрана: сущность и номера стираются, вкладка остаётся.
func perfKind(addr string) string {
	return perfDigits.ReplaceAllString(perfEntity.ReplaceAllString(addr, "@*"), "N")
}

// perfAllowed — тот же разбор, что у клиента: адрес равен приставке из списка
// сервера или начинается с неё. Пустой список — не открываем ничего.
func perfAllowed(addr string, prefixes []string) bool {
	if addr == "" || strings.Contains(addr, "&") {
		return false
	}
	for _, p := range prefixes {
		if addr == p || strings.HasPrefix(addr, p) {
			return true
		}
	}
	return false
}

type perfHit struct {
	status        int
	ttfb, total   time.Duration
	net, raw      int
	markup, build string
	timing        map[string]float64
	queries       map[string]int
	err           error
}

// perfInsecure — домашний адрес: сертификат у Сёмы дома выписан им самим, и
// проверить его цепочкой нечем. Ходим только читать, но токен всё же уходит,
// поэтому об этом говорится вслух.
func perfInsecure(base string, verify bool) bool {
	u, err := url.Parse(base)
	if err != nil || verify || u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || strings.HasSuffix(host, ".local") || net.ParseIP(host) != nil
}

func perfClient(insecure bool) *http.Client {
	return &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: insecure},
		DisableCompression: true, // сжатие просим сами: размер по сети — часть замера
	}}
}

func perfFetch(c *http.Client, o perfOptions, addr string) perfHit {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(o.base, "/")+"/ui?screen="+url.QueryEscape(addr), nil)
	if err != nil {
		return perfHit{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+o.token)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Accept-Encoding", "gzip")
	// режим подогрева: ничего наружу, а нельзя без этого — 204
	req.Header.Set("X-Sema-Prefetch", "1")
	if o.phone {
		req.Header.Set("X-Sema-Phone", "1")
	}
	started := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return perfHit{err: err, total: time.Since(started)}
	}
	defer resp.Body.Close()
	first := make([]byte, 1)
	n, _ := io.ReadFull(resp.Body, first)
	h := perfHit{status: resp.StatusCode, ttfb: time.Since(started), build: resp.Header.Get("X-Sema-Build")}
	rest, err := io.ReadAll(resp.Body)
	h.total = time.Since(started)
	if err != nil {
		h.err = err
		return h
	}
	body := append(first[:n], rest...)
	raw := body
	h.net = len(body) // маленькое сервер не сжимает: по сети идёт как есть
	if resp.Header.Get("Content-Encoding") == "gzip" {
		if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			raw, _ = io.ReadAll(zr)
		}
	}
	h.raw = len(raw)
	h.markup = string(raw)
	h.timing, h.queries = parseServerTiming(resp.Header.Get("Server-Timing"))
	return h
}

// parseServerTiming — «menu;dur=14.3;desc="19 queries", build;dur=…» в числа.
func parseServerTiming(v string) (map[string]float64, map[string]int) {
	dur, queries := map[string]float64{}, map[string]int{}
	for _, part := range strings.Split(v, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		name := strings.TrimSpace(fields[0])
		if name == "" {
			continue
		}
		for _, f := range fields[1:] {
			k, val, _ := strings.Cut(strings.TrimSpace(f), "=")
			switch k {
			case "dur":
				if d, err := strconv.ParseFloat(val, 64); err == nil {
					dur[name] = d
				}
			case "desc":
				num, _, _ := strings.Cut(strings.Trim(val, `"`), " ")
				if q, err := strconv.Atoi(num); err == nil {
					queries[name] = q
				}
			}
		}
	}
	return dur, queries
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// perfSeeds — откуда начинать: меню приходит с любым экраном, разделы
// настроек — с корнем настроек.
var perfSeeds = []string{"health", "settings"}

// perfLoad — средняя нагрузка машины, если Сёма на ней же (петля). С
// другой машины чужую нагрузку не видно, и выдумывать её нельзя.
func perfLoad(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(u.Hostname()); u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return ""
	}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return ""
	}
	return strings.Join(f[:3], " ")
}

func perfCrawl(o perfOptions) (perfRun, error) {
	insecure := perfInsecure(o.base, o.verify)
	if insecure {
		fmt.Fprintf(o.out, "сертификат %s не проверяется: домашний адрес, свой сертификат Сёмы (-verify — проверять)\n", o.base)
	}
	c := perfClient(insecure)
	run := perfRun{At: time.Now().Format(time.RFC3339), Base: o.base, Phone: o.phone}
	run.Load[0] = perfLoad(o.base)
	// список безопасных экранов называет сервер: он же решает, что можно
	// подогревать при наведении. Берём его с первого экрана, а сам этот
	// запрос засчитываем первым открытием семени — иначе оно уже не холодное
	probe := perfFetch(c, o, perfSeeds[0])
	if probe.err != nil {
		return run, fmt.Errorf("Сёма не ответил: %w", probe.err)
	}
	if probe.status != http.StatusOK {
		return run, fmt.Errorf("Сёма ответил %d — токен не подошёл, нет права «экраны» или сборка старше perf-pass 7", probe.status)
	}
	m := perfPrefetchRe.FindStringSubmatch(probe.markup)
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return run, fmt.Errorf("сервер не назвал безопасных экранов (data-prefetch) — сборка старше perf-pass 7, замер не пойдёт")
	}
	allowed := strings.Fields(html.UnescapeString(m[1]))
	run.Build = probe.build
	opened := map[string]perfHit{perfSeeds[0]: probe}

	seen := map[string]bool{}
	perKind := map[string]int{}
	queue := append([]string(nil), perfSeeds...)
	for len(queue) > 0 && len(run.Screens) < o.max {
		addr := queue[0]
		queue = queue[1:]
		if seen[addr] || !perfAllowed(addr, allowed) {
			continue
		}
		kind := perfKind(addr)
		// предел — для всего, у чего есть экземпляры: сущность или номер
		if kind != addr && perKind[kind] >= o.perKind {
			continue
		}
		seen[addr] = true
		perKind[kind]++
		var hits []perfHit
		if h, ok := opened[addr]; ok {
			hits = append(hits, h)
		}
		for len(hits) < o.repeat {
			hits = append(hits, perfFetch(c, o, addr))
			time.Sleep(o.pause)
		}
		if hits[0].status == http.StatusNoContent {
			run.Skipped = append(run.Skipped, addr)
			fmt.Fprintf(o.out, "      —  пропущен: без сети и агента показать нечего  %s\n", addr)
			continue
		}
		last := hits[len(hits)-1]
		rec := perfScreen{Screen: addr, Kind: kind, Status: http.StatusOK, FirstMs: ms(hits[0].total),
			NetKB: float64(last.net) / 1024, RawKB: float64(last.raw) / 1024,
			Elements: len(perfTag.FindAllStringIndex(last.markup, -1)),
			Timing:   last.timing, Queries: last.queries}
		var totals, ttfbs []float64
		for i, h := range hits {
			if h.err != nil && rec.Error == "" {
				rec.Error = h.err.Error()
			}
			if h.status != http.StatusOK && (rec.Status == http.StatusOK || h.status > rec.Status) {
				rec.Status = h.status
			}
			if i > 0 {
				totals = append(totals, ms(h.total))
				ttfbs = append(ttfbs, ms(h.ttfb))
			}
		}
		rec.TotalMs, rec.TTFBMs = median(totals), median(ttfbs)
		run.Screens = append(run.Screens, rec)
		mark := ""
		if !perfOK(rec) {
			mark = "  ← ответ " + strconv.Itoa(rec.Status) + " " + rec.Error
		}
		fmt.Fprintf(o.out, "%7.0f мс  перв %6.0f  %6.1f/%7.1f КБ  %5d эл  %s%s\n",
			rec.TotalMs, rec.FirstMs, rec.NetKB, rec.RawKB, rec.Elements, addr, mark)
		for _, l := range perfLinks(last.markup) {
			if !seen[l] {
				queue = append(queue, l)
			}
		}
	}
	run.Load[1] = perfLoad(o.base)
	return run, nil
}

// perfOK — экран ответил честно на все запросы: в медианы идёт только такой.
// Быстрый отказ иначе выглядел бы ускорением.
func perfOK(s perfScreen) bool { return s.Status == http.StatusOK && s.Error == "" }

func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(float64(len(s)) * q)
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

func perfSummary(w io.Writer, run perfRun) {
	var totals, firsts []float64
	var bad, ok []perfScreen
	for _, s := range run.Screens {
		if !perfOK(s) {
			bad = append(bad, s)
			continue
		}
		ok = append(ok, s)
		totals = append(totals, s.TotalMs)
		firsts = append(firsts, s.FirstMs)
	}
	fmt.Fprintf(w, "\nэкранов %d · пропущено %d · с ошибкой %d · сборка %s\n", len(ok), len(run.Skipped), len(bad), run.Build)
	if run.Load[0] != "" {
		fmt.Fprintf(w, "нагрузка машины: %s → %s (1, 5, 15 мин)\n", run.Load[0], run.Load[1])
	}
	fmt.Fprintf(w, "повторное открытие: медиана %.0f мс, p90 %.0f, максимум %.0f\n",
		median(totals), quantile(totals, .9), quantile(totals, 1))
	fmt.Fprintf(w, "первое открытие:    медиана %.0f мс, максимум %.0f\n", median(firsts), quantile(firsts, 1))
	sort.Slice(ok, func(i, j int) bool { return ok[i].TotalMs > ok[j].TotalMs })
	if len(ok) > 8 {
		ok = ok[:8]
	}
	fmt.Fprintln(w, "самые медленные (сервер: сборка / SQL, запросов):")
	for _, s := range ok {
		fmt.Fprintf(w, "  %-48s %6.0f мс   %5.0f / %5.0f, %d\n", s.Screen, s.TotalMs,
			s.Timing["build"], s.Timing["sql"], s.Queries["sql"])
	}
	for _, s := range bad {
		fmt.Fprintf(w, "  ошибка: %-40s %d %s\n", s.Screen, s.Status, s.Error)
	}
}

// perfCompare — «было → стало» по экранам, что честно ответили в обоих прогонах.
func perfCompare(w io.Writer, was, now perfRun, from string) {
	before := map[string]perfScreen{}
	for _, s := range was.Screens {
		if perfOK(s) {
			before[s.Screen] = s
		}
	}
	type diff struct {
		screen   string
		was, now float64
	}
	var common []diff
	var a, b []float64
	for _, s := range now.Screens {
		p, ok := before[s.Screen]
		if !ok || !perfOK(s) {
			continue
		}
		common = append(common, diff{s.Screen, p.TotalMs, s.TotalMs})
		a = append(a, p.TotalMs)
		b = append(b, s.TotalMs)
	}
	fmt.Fprintf(w, "\nбыло → стало (%s, сборка %s → %s), общих экранов %d:\n", filepath.Base(from), was.Build, now.Build, len(common))
	if was.Load[0] != "" || now.Load[0] != "" {
		fmt.Fprintf(w, "  нагрузка в начале прогона: %s → %s\n", orDash(was.Load[0]), orDash(now.Load[0]))
	}
	if len(common) == 0 {
		return
	}
	fmt.Fprintf(w, "  медиана %.0f → %.0f мс, p90 %.0f → %.0f, максимум %.0f → %.0f\n",
		median(a), median(b), quantile(a, .9), quantile(b, .9), quantile(a, 1), quantile(b, 1))
	sort.Slice(common, func(i, j int) bool { return common[i].now-common[i].was > common[j].now-common[j].was })
	show := func(title string, list []diff) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintln(w, "  "+title)
		for _, d := range list {
			fmt.Fprintf(w, "    %-46s %6.0f → %6.0f мс\n", d.screen, d.was, d.now)
		}
	}
	var worse, better []diff
	for _, d := range common {
		// шум петли — несколько миллисекунд; меньше 10 мс и 20 % не показываем
		change := d.now - d.was
		if change > 10 && change > d.was*0.2 {
			worse = append(worse, d)
		}
	}
	for i := len(common) - 1; i >= 0; i-- {
		d := common[i]
		if d.was-d.now > 10 && d.was-d.now > d.was*0.2 {
			better = append(better, d)
		}
	}
	if len(worse) > 10 {
		worse = worse[:10]
	}
	if len(better) > 10 {
		better = better[:10]
	}
	show("медленнее:", worse)
	show("быстрее:", better)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func readRun(path string) (perfRun, error) {
	var r perfRun
	body, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(body, &r)
}

// latestRun — последний прогон в папке; телефонные с настольными не
// сравниваются — это разные экраны.
func latestRun(dir string, phone bool) string {
	files, _ := filepath.Glob(filepath.Join(dir, "perf-*.json"))
	sort.Strings(files)
	for i := len(files) - 1; i >= 0; i-- {
		if strings.HasSuffix(files[i], "-phone.json") == phone {
			return files[i]
		}
	}
	return ""
}

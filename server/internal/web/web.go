// Пакет web — интерфейс Сёмы в браузере. Сервер отдаёт его из своего
// бинарника: рядом с исполняемым файлом ничего лежать не должно, иначе
// установка перестаёт быть одним файлом.
//
// Клиент тонкий, как и на маке: он рисует присланный документ UI-DSL и не
// знает ни про почту, ни про чеки. Новая способность появляется на
// сервере — в браузере она видна без пересборки клиента.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"
)

//go:embed static
var files embed.FS

//go:embed dictionary.txt
var dictionary string

// Dictionary — типы словаря UI-DSL, которые умеет рисовать веб-клиент:
// договор, который сверяет тест сервера, а не устная договорённость.
func Dictionary() string { return dictionary }

// build — версия сборки оболочки: отпечаток всех вшитых файлов. Ни
// номера, ни даты не нужно: изменился хоть байт — изменился отпечаток,
// а одинаковая сборка на двух машинах даёт одинаковую версию.
var build = func() string {
	names := []string{}
	_ = fs.WalkDir(files, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, _ := files.ReadFile(n)
		h.Write([]byte(n))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

// Build — версия сборки оболочки. Она же — имя кэша у служебного
// работника и заголовок X-Sema-Build у ответов: по ним клиент понимает,
// что держит устаревшую оболочку, и перезагружается.
func Build() string { return build }

// BuildHeader — заголовок с версией сборки в ответах на /ui и /do.
const BuildHeader = "X-Sema-Build"

// метка в файлах оболочки, на место которой подставляется версия сборки
const buildMark = "__СБОРКА__"

// stamped — файлы, в которых есть метка версии: они отдаются с подстановкой
var stamped = map[string]string{"sw.js": "text/javascript; charset=utf-8", "app.js": "text/javascript; charset=utf-8",
	"index.html": "text/html; charset=utf-8"}

// Handler — отдача оболочки. Всё, кроме известных файлов, отвечает
// index.html: адреса экранов живут в самом клиенте (`/#screen=inbox`), и
// перезагрузка страницы не должна давать «404».
func Handler() http.Handler {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // собранный бинарник без интерфейса — это поломка сборки
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		// имена, под которыми значок ищут сами системы, — те же файлы
		if real, ok := iconAliases[r.URL.Path]; ok {
			name = real
		}
		if _, err := fs.Stat(sub, name); err != nil {
			// не файл, а экран клиента — отдаём оболочку
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			name = "index.html"
		}
		// ничего из оболочки не кэшируется браузером без проверки: у
		// вшитых файлов нет времени изменения, и браузеру нечем понять,
		// что после обновления сервера клиент стал другим. Понять ему
		// нечем — а спросить есть чем: отпечаток сборки и есть тот
		// признак, которого не хватало. Раньше «no-cache» без него
		// означал «качай целиком каждый заход»: стили, htmx и клиент —
		// это 170 КБ на любое открытие вкладки
		w.Header().Set("Cache-Control", "no-cache")
		// шрифты — исключение: они не меняются, а перекачивать их по
		// триста килобайт на каждый заход с телефона незачем
		if strings.HasPrefix(name, "fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=604800")
		}
		// адрес с версией этой сборки (index.html просит файлы так): новая
		// сборка — новый адрес, поэтому такой ответ хранится год без проверок
		// (perf-pass 6.2). Чужая версия — как обычно, с проверкой
		// только сами файлы оболочки: страница и работник обязаны
		// проверяться каждый раз, а неизвестный путь отдаёт страницу
		if v := r.URL.Query().Get("v"); v != "" && v == build && name != "index.html" && name != "sw.js" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		etag := `"` + build + "-" + name + `"`
		w.Header().Set("ETag", etag)
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		b, err := sub.(fs.ReadFileFS).ReadFile(name)
		if err != nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		if ct, ok := stamped[name]; ok {
			w.Header().Set("Content-Type", ct)
		} else if ct := contentType(name); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		p := prepare(name, b)
		// шрифты и картинки уже сжаты — их gzip только греет процессор;
		// про «сжато в самом формате» знаем только мы. Остальное сжато
		// заранее, один раз на сборку
		if compressible(name) && len(p.raw) >= gzipMin && AcceptsGzip(r) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Add("Vary", "Accept-Encoding")
			_, _ = w.Write(p.gz)
			return
		}
		_, _ = w.Write(p.raw)
	})
}

// etagMatches — тот ли это отпечаток. Браузер шлёт список, а сильный
// отпечаток может приехать помеченным слабым (W/…) через прокси.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || part == etag || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// contentType — что это за файл. http.FileServer определял это сам; отдавая
// файлы своими руками, мы обязаны сказать это сами, иначе браузер
// откажется исполнять стили и скрипты
func contentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".json"), strings.HasSuffix(name, ".webmanifest"):
		return "application/manifest+json"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	}
	return ""
}

// compressible — есть ли смысл жать. Шрифты woff2 и картинки уже сжаты.
func compressible(name string) bool {
	switch contentType(name) {
	case "font/woff2", "", "image/png":
		return false
	case "image/svg+xml":
		return true
	}
	return !strings.HasSuffix(name, ".jpg")
}

// iconAliases — адреса значка, которые система просит сама, не заглядывая
// ни в страницу, ни в манифест: iPhone при «На экран “Домой”» стучится в
// /apple-touch-icon*.png и /favicon.ico, и без ответа значок получается
// буквой на сером. Отдаётся тот же файл, что и под своим именем; у
// /favicon.ico содержимое PNG — браузеры узнают картинку по содержимому, а
// не по расширению.
var iconAliases = map[string]string{
	"/apple-touch-icon.png":             "icon-180.png",
	"/apple-touch-icon-precomposed.png": "icon-180.png",
	"/favicon.ico":                      "icon-192.png",
}

// shellPaths — адреса оболочки, которые отдаются без входа: в них нет
// данных, а страница входа всё равно нужна раньше сессии. Считаются один
// раз: список просматривается на каждом запросе к Сёме.
var shellPaths = func() []string {
	out := []string{"/", "/index.html", "/app.js", "/htmx.js", "/style.css", "/fonts.css", "/sw.js", "/manifest.webmanifest"}
	// значки приложения — тоже оболочка, и все разом: iPhone просит
	// значок без cookie (до входа, из шторки «На экран “Домой”»), а в
	// файле ничего, кроме картинки, нет. Берутся по имени `icon*` из самой
	// папки, а не списком: новый размер не должен молча оказаться за
	// входом, пока кто-нибудь не заметит пустой значок
	if ents, err := fs.ReadDir(files, "static"); err == nil {
		for _, e := range ents {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "icon") {
				out = append(out, "/"+e.Name())
			}
		}
	}
	for alias := range iconAliases {
		out = append(out, alias)
	}
	// сами файлы шрифтов — тоже часть оболочки: без них страница входа
	// осталась бы системным шрифтом, а вход бывает раньше сессии
	_ = fs.WalkDir(files, "static/fonts", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, "/"+strings.TrimPrefix(p, "static/"))
		}
		return nil
	})
	sort.Strings(out)
	return out
}()

// Paths — адреса оболочки, которые отдаются без входа.
func Paths() []string { return append([]string(nil), shellPaths...) }

var shellSet = func() map[string]bool {
	set := make(map[string]bool, len(shellPaths))
	for _, p := range shellPaths {
		set[p] = true
	}
	return set
}()

// IsShell — это адрес оболочки: ровно один из перечисленных в Paths.
func IsShell(path string) bool { return shellSet[path] }

// shellFile — файл оболочки, готовый к отдаче: метка сборки подставлена,
// комментарии вырезаны (strip.go), сжатие gzip(9) сделано один раз. Раньше
// каждый запрос сжимался заново быстрым уровнем: 147 КБ по сети на холодный
// заход вместо 58 (perf-pass 6.1).
type shellFile struct{ raw, gz []byte }

var shellFiles sync.Map // имя → *shellFile

func prepare(name string, b []byte) *shellFile {
	if v, ok := shellFiles.Load(name); ok {
		return v.(*shellFile)
	}
	if _, ok := stamped[name]; ok {
		b = bytes.ReplaceAll(b, []byte(buildMark), []byte(build))
	}
	switch {
	case name == "htmx.js":
		// уже сжат сборкой htmx: разбирать нечего
	case strings.HasSuffix(name, ".js"):
		b = stripJS(b)
	case strings.HasSuffix(name, ".css"):
		b = stripCSS(b)
	}
	f := &shellFile{raw: b}
	if compressible(name) && len(b) >= gzipMin {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(b)
		_ = zw.Close()
		f.gz = buf.Bytes()
	}
	v, _ := shellFiles.LoadOrStore(name, f)
	return v.(*shellFile)
}

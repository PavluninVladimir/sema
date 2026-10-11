package web

// Сжатие ответов. Описание экрана — это JSON в сотни килобайт («Память» —
// 172 КБ), и по сети до Pi или через прокси оно едет целиком каждый раз,
// когда экран поменялся. Gzip сокращает его в разы, а стоит одного
// прохода по буферу.

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// gzipMin — ниже этого порога сжимать дороже, чем отдать как есть.
const gzipMin = 2048

var gzipPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed); return w }}

// WriteMaybeGzip отдаёт тело сжатым, если клиент это умеет и тело того
// стоит. Заголовки (в том числе ETag) ставятся до вызова.
func WriteMaybeGzip(w http.ResponseWriter, r *http.Request, body []byte) {
	if len(body) < gzipMin || !AcceptsGzip(r) {
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	// длина тела меняется — иначе клиент ждёт байты, которых не будет
	w.Header().Del("Content-Length")
	w.Header().Add("Vary", "Accept-Encoding")
	zw := gzipPool.Get().(*gzip.Writer)
	defer gzipPool.Put(zw)
	zw.Reset(w)
	if _, err := zw.Write(body); err != nil {
		return
	}
	_ = zw.Close()
}

// AcceptsGzip — умеет ли клиент распаковать ответ.
func AcceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}

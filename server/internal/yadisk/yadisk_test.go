package yadisk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Токен уходит заголовком и не должен попадать ни в адрес, ни в тело:
// адреса видны в журналах промежуточных узлов.
func TestTokenOnlyInHeader(t *testing.T) {
	var sawAddr, sawHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAddr = r.URL.String()
		sawHeader = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"used_space":1,"total_space":2}`))
	}))
	defer srv.Close()
	c := New("секретный-токен")
	c.http = srv.Client()
	req, err := c.request(context.Background(), "GET", srv.URL+"/v1/disk", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.readBody(req, "проба"); err != nil {
		t.Fatal(err)
	}
	if sawHeader != "OAuth секретный-токен" {
		t.Errorf("токен не в заголовке: %q", sawHeader)
	}
	if strings.Contains(sawAddr, "секретный-токен") {
		t.Errorf("токен уехал в адрес: %q", sawAddr)
	}
}

// Ошибка Диска должна доходить словами. «500 Internal Server Error» без
// объяснения — это ровно то, из-за чего 11 сентября 2026 час искали
// беду не там.
func TestDiskErrorInWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Не удалось найти запрошенный ресурс.","error":"DiskNotFoundError"}`))
	}))
	defer srv.Close()
	c := New("т")
	c.http = srv.Client()
	req, _ := c.request(context.Background(), "GET", srv.URL+"/x", nil)
	_, err := c.readBody(req, "Диск не рассказал о файле")
	if err == nil {
		t.Fatal("ошибки нет")
	}
	if !strings.Contains(err.Error(), "Не удалось найти") {
		t.Errorf("причина не дошла до человека: %v", err)
	}
}

// Папка заводится по уровням: Диск не создаёт промежуточные сам, и
// «Сёма/копии» без «Сёма» кончается отказом.
func TestFolderByLevels(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Query().Get("path"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := NewForTests(srv.URL, "т")
	c.http = srv.Client()
	if err := c.CreateFolder(context.Background(), "Сёма/копии"); err != nil {
		t.Fatal(err)
	}
	want := []string{"disk:/Сёма", "disk:/Сёма/копии"}
	if len(paths) != len(want) {
		t.Fatalf("уровней %d: %v", len(paths), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("уровень %d: %q, ждали %q", i, paths[i], want[i])
		}
	}
}

// Уже существующая папка — не ошибка: копия кладётся в неё каждый день.
func TestFolderAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Ресурс уже существует"}`))
	}))
	defer srv.Close()
	c := NewForTests(srv.URL, "т")
	c.http = srv.Client()
	if err := c.CreateFolder(context.Background(), "Сёма/копии"); err != nil {
		t.Errorf("существующая папка объявлена бедой: %v", err)
	}
}

// Сведения — тот самый ответ, ради которого всё затевалось: размер и
// sha256 с самого Диска.
func TestStatReturnsHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Entry{Name: "к.tgz", Size: 42, Hash: "abc", Kind: "file"})
	}))
	defer srv.Close()
	c := NewForTests(srv.URL, "т")
	c.http = srv.Client()
	rec, err := c.Stat(context.Background(), "Сёма/копии/к.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Size != 42 || rec.Hash != "abc" {
		t.Errorf("Диск рассказал не то: %+v", rec)
	}
}

// УзелЗагрузки называет, куда именно уехало тело: оно идёт не туда же,
// куда остальные запросы, и при беде это первый вопрос.
func TestUploadNodeFromLink(t *testing.T) {
	if name := UploadNode("https://uploader55sas.disk.yandex.net:443/upload-target/xyz"); name != "uploader55sas.disk.yandex.net" {
		t.Errorf("узел %q", name)
	}
	if name := UploadNode("::не адрес::"); name == "" {
		t.Error("на непонятной ссылке имя должно быть хоть каким-то")
	}
}

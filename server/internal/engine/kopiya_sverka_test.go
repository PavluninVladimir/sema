package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/yadisk"
)

// Поддельный Диск: принимает тело, помнит его и отвечает так, как велят.
type fakeDisk struct {
	host     *httptest.Server
	accepted map[string][]byte
	breakOn  bool // оборвать ответ после того, как тело принято целиком
	notStore bool // принять тело и не сохранить: вот это настоящая потеря
}

func raiseDisk(t *testing.T, shouldBreak bool) *fakeDisk {
	t.Helper()
	d := &fakeDisk{accepted: map[string][]byte{}, breakOn: shouldBreak}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/disk/resources/upload", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"href": d.host.URL + "/upload?path=" + path, "method": "PUT"})
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0, 1024)
		buffer := make([]byte, 512)
		for {
			n, err := r.Body.Read(buffer)
			body = append(body, buffer[:n]...)
			if err != nil {
				break
			}
		}
		// тело принято целиком и СОХРАНЕНО — ровно как у настоящего
		// Яндекса 11 сентября 2026
		if !d.notStore {
			d.accepted[r.URL.Query().Get("path")] = body
		}
		if d.breakOn {
			// обрыв вместо ответа
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/v1/disk/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		path := r.URL.Query().Get("path")
		body, has := d.accepted[path]
		if !has {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Не удалось найти запрошенный ресурс."}`))
			return
		}
		amount := sha256.Sum256(body)
		_ = json.NewEncoder(w).Encode(yadisk.Entry{Name: filepath.Base(path),
			Size: int64(len(body)), Hash: hex.EncodeToString(amount[:]), Kind: "file"})
	})
	mux.HandleFunc("/v1/disk", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"used_space":1,"total_space":1000000000}`))
	})
	d.host = httptest.NewServer(mux)
	t.Cleanup(d.host.Close)
	return d
}

// backupOnDisk — файл копии и хэш рядом, как их кладёт снимок.
func backupOnDisk(t *testing.T, content string) (catalog, path, name string) {
	t.Helper()
	catalog = t.TempDir()
	name = "сёма-2026-09-11-1401.tgz"
	path = filepath.Join(catalog, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	amount := sha256.Sum256([]byte(content))
	if err := os.WriteFile(path+".sha256",
		[]byte(hex.EncodeToString(amount[:])+"  "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return
}

// Главный случай, ради которого всё переписано. 11 сентября 2026 Яндекс
// принял копию целиком, сохранил её и не ответил — Сёма объявила копию
// потерянной, подняла тревогу и сняла новую. Четыре раза за день, при
// живых копиях на Диске.
//
// Обрыв ответа при целом файле — не потеря.
func TestBrokenReplyOnWholeBackupNotTrouble(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	d := raiseDisk(t, true) // рвать ответ
	k := yadisk.NewForTests(d.host.URL, "токен")
	_, path, name := backupOnDisk(t, strings.Repeat("копия-", 500))

	result, err := e.takeBackup(context.Background(), k, "Сёма/копии", path, name)
	if err != nil {
		t.Fatalf("копия объявлена потерянной, хотя она на Диске: %v", err)
	}
	if !strings.Contains(result, "сверена на Диске") {
		t.Errorf("итог не говорит, что проверяли у Диска: %q", result)
	}
	// и человек должен узнать, что ответа не было: это не норма
	if !strings.Contains(result, "ответ на заливку не пришёл") {
		t.Errorf("обрыв ответа замолчали: %q", result)
	}
}

// Обычный случай: ответ пришёл, файл сошёлся.
func TestBackupLeftAndVerified(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	d := raiseDisk(t, false)
	k := yadisk.NewForTests(d.host.URL, "токен")
	_, path, name := backupOnDisk(t, "содержимое копии")

	result, err := e.takeBackup(context.Background(), k, "Сёма/копии", path, name)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "ответ на заливку не пришёл") {
		t.Errorf("выдумали обрыв: %q", result)
	}
	if !strings.Contains(result, "сверена на Диске") {
		t.Errorf("итог: %q", result)
	}
}

// А вот если на Диске лежит не то — это беда, и молчать о ней нельзя.
// Иначе проверка превращается в обряд.
func TestFailedHashIsTrouble(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	d := raiseDisk(t, false)
	k := yadisk.NewForTests(d.host.URL, "токен")
	_, path, name := backupOnDisk(t, "настоящее содержимое")
	// подменяем хэш рядом: будто копия на Диске не та
	amount := sha256.Sum256([]byte("совсем другое"))
	if err := os.WriteFile(path+".sha256",
		[]byte(hex.EncodeToString(amount[:])+"  "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.takeBackup(context.Background(), k, "Сёма/копии", path, name)
	if err == nil {
		t.Fatal("несошедшийся sha256 не замечен")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("причина не названа: %v", err)
	}
}

// Файла на Диске нет вовсе — тогда беда настоящая, и в ней должно быть
// сказано и про заливку, и про то, что Диск о копии не знает.
func TestNoOnDiskIsLoss(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	d := raiseDisk(t, false)
	d.notStore = true // принял тело и потерял
	k := yadisk.NewForTests(d.host.URL, "токен")
	_, path, name := backupOnDisk(t, "копия")
	_, err := e.takeBackup(context.Background(), k, "Сёма/копии", path, name)
	if err == nil {
		t.Fatal("потеря копии не замечена")
	}
	// в беде должно быть сказано и про заливку, и про то, что Диск о
	// копии не знает: одного «не уехала» мало, чтобы понять, где искать
	if !strings.Contains(err.Error(), "на Диске её нет") && !strings.Contains(err.Error(), "Диск о ней не рассказал") {
		t.Errorf("беда не объясняет, что случилось: %v", err)
	}
}

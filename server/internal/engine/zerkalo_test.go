package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// harmfulMirror — хранилище, которое роняет один назначенный ключ и
// принимает остальные.
type harmfulMirror struct {
	name     string
	fails    string
	accepted map[string]int
}

func (x *harmfulMirror) Name() string { return x.name }
func (x *harmfulMirror) Kind() string { return "тест" }
func (x *harmfulMirror) Path(string) string {
	return ""
}
func (x *harmfulMirror) Get(string) ([]byte, error) {
	return nil, fmt.Errorf("не нужно")
}
func (x *harmfulMirror) Delete(string) error { return nil }
func (x *harmfulMirror) Stat() (int, int64)  { return len(x.accepted), 0 }
func (x *harmfulMirror) Put(key string, data []byte) error {
	if key == x.fails {
		return fmt.Errorf("dial tcp 203.0.113.15:443: i/o timeout")
	}
	x.accepted[key]++
	return nil
}

// Один файл, который не уходит, не должен запирать зеркало.
//
// 11 сентября 2026 зеркало догоняло 260 файлов и на 133-м получило
// «i/o timeout» от узла загрузки. Проход обрывался целиком и начинался
// заново с того же файла. Разовую сетевую беду это ещё лечит следующей
// минутой, а файл, который не уйдёт никогда, запер бы и себя, и все
// следующие — навсегда и молча.
func TestOneBadFileNotLocksMirror(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	var keys []string
	for i := 0; i < 5; i++ {
		a, _, err := e.AddFile(context.Background(), fmt.Sprintf("файл-%d.txt", i), "text/plain",
			[]byte(fmt.Sprintf("содержимое %d", i)), FileSource{Kind: store.SourceUpload}, nil)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, a.Key)
	}
	harmful := &harmfulMirror{name: "зеркало", fails: keys[1], accepted: map[string]int{}}

	// судим настоящий проход, а не его пересказ в тесте
	result := e.mirrorBatch(context.Background(), "зеркало", harmful)

	if !strings.Contains(result, "уложено 4") {
		t.Errorf("плохой сосед утянул за собой остальных: %q", result)
	}
	// и о неудаче должно быть сказано числом и причиной: «уложено 4»
	// без остатка читается как полный успех
	if !strings.Contains(result, "не вышло 1") {
		t.Errorf("число неудач не названо: %q", result)
	}
	if !strings.Contains(result, "i/o timeout") {
		t.Errorf("причина не названа: %q", result)
	}
	// упавший файл не помечен уехавшим — иначе он потеряется навсегда
	for _, a := range mustFiles(t, st) {
		if a.Key == keys[1] && a.Mirror != "" {
			t.Error("файл, который не уехал, помечен уехавшим — это потеря")
		}
	}
}

// mustFiles — все файлы из базы.
func mustFiles(t *testing.T, st *store.Store) []*store.Attachment {
	t.Helper()
	list, err := st.FilesToMirror("другое-зеркало", 100)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

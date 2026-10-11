package store

import (
	"math/rand"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

// Проверки хранилища на сгенерированных случаях. Главное свойство очереди —
// «ничего не теряется»: сколько работ поставили, столько и должно найтись
// в какой-нибудь колонке сводки.

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// Свойство очереди: при любой последовательности взятий, неудач и завершений
// сумма по состояниям равна числу поставленных работ.
func TestPropertyQueueLosesNothing(t *testing.T) {
	st := testStore(t)
	rnd := rand.New(rand.NewSource(90420261))
	kinds := []string{"разбор", "память", "чек"}
	placed := map[string]bool{}
	for step := 0; step < 400; step++ {
		switch rnd.Intn(4) {
		case 0, 1: // поставить работу
			k := kinds[rnd.Intn(len(kinds))]
			ref := strconv.Itoa(rnd.Intn(25))
			if err := st.Enqueue(k, ref, "", rnd.Intn(9)); err != nil {
				t.Fatalf("постановка: %v", err)
			}
			placed[k+"/"+ref] = true
		case 2: // взять и завершить
			items, err := st.TakeWork(1+rnd.Intn(3), time.Minute)
			if err != nil {
				t.Fatalf("взятие: %v", err)
			}
			for _, it := range items {
				if err := st.FinishWork(it.ID); err != nil {
					t.Fatalf("завершение: %v", err)
				}
				delete(placed, it.Kind+"/"+it.Ref)
			}
		case 3: // взять и уронить
			items, err := st.TakeWork(2, time.Minute)
			if err != nil {
				t.Fatalf("взятие: %v", err)
			}
			for _, it := range items {
				if _, err := st.FailWork(it.ID, "модель молчит", time.Now().Add(-time.Second), 3); err != nil {
					t.Fatalf("неудача: %v", err)
				}
			}
		}
		stats, err := st.WorkStats()
		if err != nil {
			t.Fatalf("сводка: %v", err)
		}
		var alive int
		for _, s := range stats {
			alive += s.Waiting + s.Running + s.Held
		}
		if alive != len(placed) {
			t.Fatalf("шаг %d: в очереди %d работ, ожидали %d", step, alive, len(placed))
		}
	}
}

// Повторная постановка той же работы не плодит строк: иначе после сбоя
// очередь распухает дублями одного письма.
func TestPropertyEnqueueIsIdempotent(t *testing.T) {
	st := testStore(t)
	for i := 0; i < 50; i++ {
		if err := st.Enqueue("разбор", "77", "", i%9); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.WorkList("разбор", WorkWaiting, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("в очереди %d строк, ожидали одну", len(list))
	}
	if !st.HasWork("разбор", "77") {
		t.Error("HasWork не видит поставленную работу")
	}
}

// Работа, у которой не вышел срок, не берётся: так растущая пауза после
// неудачи действительно работает.
func TestQueueRespectsNotBefore(t *testing.T) {
	st := testStore(t)
	if err := st.Enqueue("разбор", "1", "", 5); err != nil {
		t.Fatal(err)
	}
	items, _ := st.TakeWork(5, time.Minute)
	if len(items) != 1 {
		t.Fatalf("взяли %d работ", len(items))
	}
	if _, err := st.FailWork(items[0].ID, "не вышло", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	again, _ := st.TakeWork(5, time.Minute)
	if len(again) != 0 {
		t.Fatalf("работа с будущим сроком взята: %+v", again[0])
	}
}

// Значение секрета не хранится в базе открытым текстом ни при какой длине и
// ни при каком содержимом.
func TestPropertySecretsNeverStoredPlain(t *testing.T) {
	st := testStore(t)
	box, err := secret.New("ключ-для-тестов-достаточной-длины")
	if err != nil {
		t.Fatalf("ключ: %v", err)
	}
	st.UseSecretBox(box)
	rnd := rand.New(rand.NewSource(4092026))
	alphabet := []rune("абвгдеёжзqwertyuiop0123456789-_/+= \n\t")
	for i := 0; i < 200; i++ {
		n := 8 + rnd.Intn(120) // короче 8 знаков совпадение с шифром случайно
		var sb strings.Builder
		for j := 0; j < n; j++ {
			sb.WriteRune(alphabet[rnd.Intn(len(alphabet))])
		}
		value := sb.String()
		name := "ssh:проба-" + strconv.Itoa(i)
		if err := st.SaveSecret(&Secret{Name: name, Kind: SecretSSH, Note: "тест"}, value); err != nil {
			t.Fatalf("запись секрета: %v", err)
		}
		var raw string
		if err := st.db.QueryRow(`SELECT value FROM secrets WHERE name = ?`, name).Scan(&raw); err != nil {
			t.Fatalf("чтение строки: %v", err)
		}
		if !secret.Encrypted(raw) {
			t.Fatalf("секрет %q лежит без шифра", name)
		}
		if strings.Contains(raw, value) {
			t.Fatalf("значение видно в базе: %q", raw)
		}
		got, ok := st.SecretValue(name)
		if !ok || got != value {
			t.Fatalf("секрет не читается обратно: %q против %q", got, value)
		}
	}
}

// Без ключа шифрования запись секрета отклоняется: лучше отказ, чем ключ SSH
// открытым текстом в базе.
func TestSecretRefusedWithoutKey(t *testing.T) {
	st := testStore(t)
	if err := st.SaveSecret(&Secret{Name: "ssh:без-ключа", Kind: SecretSSH}, "приватный ключ"); err == nil {
		t.Fatal("секрет записан без ключа шифрования")
	}
}

// Журнал отправленного: фильтры отдают только подходящие строки.
func TestSentListFilters(t *testing.T) {
	st := testStore(t)
	rows := []*SentRow{
		{Consumer: "core:mail", Model: "локальная", Outside: false, Class: ClassPlain, Preview: "разбор письма", OK: true},
		{Consumer: "chat:1", Model: "облачная", Outside: true, Class: ClassSensitive, Preview: "счёт на оплату", OK: true},
		{Consumer: "core:memory", Model: "облачная", Outside: true, Class: ClassSecret, Preview: "паспорт", OK: false},
	}
	for _, r := range rows {
		if err := st.AddSent(r); err != nil {
			t.Fatalf("запись: %v", err)
		}
	}
	all, _ := st.SentList(false, "", "", 10)
	if len(all) != 3 {
		t.Fatalf("всего %d строк", len(all))
	}
	outside, _ := st.SentList(true, "", "", 10)
	if len(outside) != 2 {
		t.Fatalf("наружу %d строк, ожидали две", len(outside))
	}
	secretOnly, _ := st.SentList(false, ClassSecret, "", 10)
	if len(secretOnly) != 1 || secretOnly[0].Consumer != "core:memory" {
		t.Fatalf("секретных %d строк: %+v", len(secretOnly), secretOnly)
	}
	found, _ := st.SentList(false, "", "счёт", 10)
	if len(found) != 1 || found[0].Consumer != "chat:1" {
		t.Fatalf("поиск дал %d строк", len(found))
	}
	stat := st.SentStats(time.Now().Add(-time.Hour).Format(time.RFC3339))
	if stat.Calls != 3 || stat.Outside != 2 || stat.Sensitive != 2 || stat.Fails != 1 {
		t.Fatalf("сводка: %+v", stat)
	}
}

// Одну и ту же работу нельзя выдать дважды: два прохода очереди, идущие
// одновременно, иначе делали её вдвоём.
func TestTakeWorkHandsItemOnce(t *testing.T) {
	st := testStore(t)
	for i := 0; i < 20; i++ {
		if err := st.Enqueue("разбор", strconv.Itoa(i), "", 5); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				items, err := st.TakeWork(5, time.Minute)
				if err != nil {
					return
				}
				mu.Lock()
				for _, it := range items {
					if seen[it.ID] {
						t.Errorf("работа %d выдана дважды", it.ID)
					}
					seen[it.ID] = true
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
}

// Без ключа шифрования чтение зашифрованного значения не роняет сервер, а
// честно говорит, что читать нечем.
func TestReadingSealedValueWithoutKeyDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.New("ключ-который-мы-потом-потеряем")
	if err != nil {
		t.Fatal(err)
	}
	st.UseSecretBox(box)
	if err := st.SaveSecret(&Secret{Name: "ssh:проба", Kind: SecretSSH}, "приватный ключ"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	// сервер подняли без SEMA_KEY: значение в базе зашифровано, ключа нет
	again, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	v, ok := again.SecretValue("ssh:проба")
	if v != "" || ok {
		t.Fatalf("без ключа вернули значение: %q", v)
	}
	if again.SecretProblem() == "" {
		t.Error("человеку не объяснили, почему секрет не читается")
	}
}

// Сессия Telegram не ложится в базу открытой: без ключа шифрования запись
// отклоняется — как и у секретов.
func TestTGSessionRefusedWithoutKey(t *testing.T) {
	st := testStore(t) // без UseSecretBox
	err := st.SaveTGAccount(&TGAccount{Phone: "+79990000000", State: "ok", Session: "секретная-сессия"})
	if err == nil {
		t.Fatal("сессия записана без ключа шифрования")
	}
	if !strings.Contains(err.Error(), "ключа шифрования") {
		t.Errorf("причина отказа: %v", err)
	}
	// аккаунт без сессии писать можно: шага входа ещё не было
	if err := st.SaveTGAccount(&TGAccount{Phone: "+79990000000", State: "new"}); err != nil {
		t.Errorf("аккаунт без сессии не сохранился: %v", err)
	}
}

// Сервер, поднятый с чужим ключом, не должен затирать уже лежащие секреты:
// так корень для агентов и сессия Telegram пропали 5 сентября 2026 — Сёма
// не смог их прочитать и завела новые поверх.
func TestWrongKeyDoesNotOverwriteSecret(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.New("настоящий-ключ")
	if err != nil {
		t.Fatal(err)
	}
	st.UseSecretBox(box)
	if err := st.SaveSecret(&Secret{Name: "agents:root-key", Kind: SecretSSH}, "корень"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	again, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	other, err := secret.New("чужой-ключ")
	if err != nil {
		t.Fatal(err)
	}
	again.UseSecretBox(other)
	if err := again.SaveSecret(&Secret{Name: "agents:root-key", Kind: SecretSSH}, "новый корень"); err == nil {
		t.Fatal("чужим ключом переписали существующий секрет")
	}

	// вернулись с настоящим ключом — секрет на месте
	back, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	right, err := secret.New("настоящий-ключ")
	if err != nil {
		t.Fatal(err)
	}
	back.UseSecretBox(right)
	if v, ok := back.SecretValue("agents:root-key"); !ok || v != "корень" {
		t.Fatalf("секрет потерян: %q", v)
	}
}

// FilterExcludedAttachments (offline-snapshot 2а.6): пустой Account у
// вложения значит одно из двух — «письмо старого закрытого ящика,
// сохранённое до появления имён» и «файл вообще без письма» (загрузка,
// наблюдаемая папка, модуль). Ровно поэтому отсев идёт после выборки по
// MessageID, а не SQL-исключением по имени ящика: закрой ящик с пустым
// именем (`excluded[""] == true`, старые письма основного ящика лежат
// именно так) — и SQL срезал бы заодно все загруженные человеком файлы.
func TestFilterExcludedAttachmentsKeepsFileWithoutMessage(t *testing.T) {
	st := testStore(t)
	ex := &Exclusion{Accounts: map[string]bool{"закрытый": true, "": true}} // "" — старый закрытый основной ящик
	atts := []*Attachment{
		{ID: 1, MessageID: 10, Account: "закрытый"}, // письмо закрытого ящика — уходит
		{ID: 2, MessageID: 11, Account: ""},         // старое письмо закрытого основного ящика — уходит
		{ID: 3, MessageID: 0, Account: ""},          // загруженный файл, письма нет вовсе — остаётся
		{ID: 4, MessageID: 12, Account: "открытый"}, // письмо открытого ящика — остаётся
	}
	out := st.FilterExcludedAttachments(atts, ex)
	var ids []int64
	for _, a := range out {
		ids = append(ids, a.ID)
	}
	if len(ids) != 2 || ids[0] != 3 || ids[1] != 4 {
		t.Fatalf("отсев вложений: получили %v, ждали [3 4]", ids)
	}
}

package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Хранилище на Яндекс.Диске рисуется строкой зеркала, а не «тип не
// поддерживается»: движок этот вид знает, а экран знал только S3, и у
// строки не было ни «Проверить», ни «Удалить».
func TestYandexDiskRendersAsMirror(t *testing.T) {
	s, st := testServer(t)
	conf, _ := json.Marshal(engine.YaDiskConf{
		Token: "YaDiskToken", Folder: "Сёма",
	})
	if err := st.AddStorage("яндекс-диск", "yadisk", string(conf)); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("settings:storages", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	строка := findRow(doc, "storage-яндекс-диск")
	if строка == nil {
		t.Fatal("хранилища нет на экране")
	}
	if strings.Contains(строка.Caption, "не поддерживается") {
		t.Errorf("вид, который движок умеет, назван неподдерживаемым: %q", строка.Caption)
	}
	// в строке — папка на Диске, а какой секрет с токеном, видно на экране хранилища
	if !strings.Contains(строка.Caption, "Сёма") {
		t.Errorf("в подписи нет папки на Диске: %q", строка.Caption)
	}
	one, err := s.buildScreen(storageAddr("яндекс-диск"), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dumpScreen(one), "YaDiskToken") {
		t.Error("на экране хранилища нет имени секрета с токеном")
	}
	// действия на месте: без них хранилище нельзя ни проверить, ни убрать
	действия := map[string]bool{}
	for _, д := range строка.Actions {
		действия[д.Action] = true
	}
	for _, д := range строка.Menu {
		действия[д.Action] = true
	}
	for _, надо := range []string{"check_storage", "delete_storage"} {
		if !действия[надо] {
			t.Errorf("нет действия %q", надо)
		}
	}
	has := false
	for _, к := range строка.Badges {
		if к.Text == "зеркало" {
			has = true
		}
	}
	if !has {
		t.Error("нет капсулы «зеркало»")
	}
}

// Незнакомый вид по-прежнему говорит правду и не даёт кнопок: нажимать
// на нём нечего.
func TestUnknownStorageKindSaysPlainly(t *testing.T) {
	s, st := testServer(t)
	if err := st.AddStorage("нечто", "ftp", "{}"); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("settings:storages", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	строка := findRow(doc, "storage-нечто")
	if строка == nil {
		t.Fatal("строки нет")
	}
	if !strings.Contains(строка.Caption, "не поддерживается") {
		t.Errorf("незнакомый вид должен сказать об этом: %q", строка.Caption)
	}
	// в строке только переход на экран хранилища: проверять и писать в такое нечем
	for _, a := range строка.Actions {
		if a.Action != "$goto" {
			t.Errorf("у незнакомого вида в строке не должно быть кнопок, а есть %q", a.Action)
		}
	}
	if len(строка.Menu) > 0 {
		t.Errorf("у незнакомого вида в строке нет меню: %+v", строка.Menu)
	}
	// убрать запись можно с экрана хранилища, «Опасным» с вопросом: она осталась от прежних версий
	one, err := s.buildScreen(storageAddr("нечто"), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	danger := findRow(one, "danger-remove")
	if danger == nil || len(danger.Actions) != 1 || danger.Actions[0].Action != "delete_storage" || danger.Actions[0].Confirm == "" {
		t.Errorf("запись незнакомого вида должна убираться с экрана хранилища, с вопросом: %+v", danger)
	}
	if findRow(one, "storage-check") != nil {
		t.Error("у незнакомого вида проверки нет, а строка про неё есть")
	}
}

func findRow(doc *Document, id string) *Component {
	var walk func([]Component) *Component
	walk = func(cs []Component) *Component {
		for i := range cs {
			if cs[i].ID == id {
				return &cs[i]
			}
			if в := walk(cs[i].Children); в != nil {
				return в
			}
		}
		return nil
	}
	return walk(doc.Components)
}

// Хранилище без токена не работает вовсе, и экран обязан сказать это
// словами. Молчаливое «зеркало» при мёртвом хранилище — это ровно та
// беда, из-за которой 260 файлов месяц никуда не зеркалились, а на
// экране всё выглядело в порядке.
func TestDiskWithoutTokenSaysSo(t *testing.T) {
	s, st := testServer(t)
	conf, _ := json.Marshal(engine.YaDiskConf{Folder: "Сёма"})
	if err := st.AddStorage("диск-без-токена", "yadisk", string(conf)); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("settings:storages", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	строка := findRow(doc, "storage-диск-без-токена")
	if строка == nil {
		t.Fatal("хранилища нет на экране")
	}
	if !strings.Contains(строка.Caption, "токена нет") {
		t.Errorf("про отсутствующий токен не сказано: %q", строка.Caption)
	}
}

// Старая запись вида «webdav» после переезда должна читаться как
// Яндекс.Диск, а не как «тип не поддерживается»: у человека она уже
// заведена, и ломать её переездом нельзя.
func TestOldWebdavRecordIsRead(t *testing.T) {
	s, st := testServer(t)
	conf, _ := json.Marshal(engine.YaDiskConf{Token: "YaDiskToken", Folder: "Сёма"})
	if err := st.AddStorage("старый-диск", "webdav", string(conf)); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("settings:storages", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	строка := findRow(doc, "storage-старый-диск")
	if строка == nil {
		t.Fatal("хранилища нет на экране")
	}
	if strings.Contains(строка.Caption, "не поддерживается") {
		t.Errorf("старая запись объявлена неподдерживаемой: %q", строка.Caption)
	}
}

// Зеркало месяц стояло выключенным, и на экране это выглядело как
// порядок: строка «зеркало» была, а числа не было. Выключенное зеркало
// при живых файлах обязано сказать, что ни один из них никуда не уехал.
func TestDisabledMirrorTellsTruth(t *testing.T) {
	s, st := testServer(t)
	conf, _ := json.Marshal(engine.YaDiskConf{Token: "YaDiskToken", Folder: "Сёма"})
	if err := st.AddStorage("яндекс-диск", "yadisk", string(conf)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.eng.AddFile(context.Background(), "счёт.pdf", "application/pdf",
		[]byte("содержимое счёта"), engine.FileSource{Kind: store.SourceUpload}, nil); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("settings:storages~sources", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	строка := findRow(doc, "src-mirror")
	if строка == nil {
		t.Fatal("строки зеркала нет на «Хранилищах» — его состояние опять негде увидеть")
	}
	if строка.Toggle != "off" {
		t.Errorf("зеркало показано включённым: %q", строка.Toggle)
	}
	if !strings.Contains(строка.Caption, "ни один") {
		t.Errorf("выключенное зеркало не сказало, что файлы никуда не уехали: %q", строка.Caption)
	}
}

// Тумблер щёлкает обе стороны: и настройку, и саму задачу. Иначе
// повторяется история «Копии наружу» — на экране включено, а задача
// стоит.
func TestMirrorToggleClicksTask(t *testing.T) {
	s, st := testServer(t)
	// задача заводится сердцебиением при старте; в тесте его нет
	if err := st.CreateTask(&store.Task{Title: "Зеркало файлов",
		System: "files_mirror", Schedule: "каждые 1 мин"}); err != nil {
		t.Fatal(err)
	}
	if s.eng.MirrorOn() || s.eng.MirrorTaskOn() {
		t.Fatal("зеркало включено с самого начала")
	}
	if _, err := s.execute(actionRequest{Action: "toggle_mirror"}); err != nil {
		t.Fatal(err)
	}
	if !s.eng.MirrorOn() {
		t.Error("настройка не включилась")
	}
	if !s.eng.MirrorTaskOn() {
		t.Error("настройка включилась, а задача осталась выключенной — ровно та беда, из-за которой копии не было")
	}
}

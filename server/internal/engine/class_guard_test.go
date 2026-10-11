package engine

import (
	"github.com/PavluninVladimir/sema/server/internal/llm"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Машиночитаемая строка паспорта — самая надёжная примета документа на
// фотографии: имя файла не говорит ничего, а её tesseract вытаскивает даже
// из мусорного распознавания.
func TestMRZRaisesClass(t *testing.T) {
	trash := "MOCKBA\n||| 4507 ###\nP<RUSIVANOV<<IVAN<<<<<<<<<<<<<<<<<<<<<<<<<<<\n4507123456RUS8501014M"
	if got := sentClass(trash); got != store.ClassSecret {
		t.Fatalf("паспорт в выводе OCR должен быть секретным, а вышло %q", got)
	}
	if got := sentClass("Привет! Отправляю фотки с дачи, погода была чудесная."); got != store.ClassPlain {
		t.Fatalf("обычный текст стал %q", got)
	}
}

// Приметы документов не должны срабатывать на любое упоминание: слово в
// обычной переписке — повод для чувствительного, а не для секретного.
func TestSignsDocuments(t *testing.T) {
	secret := []string{
		"скан водительского удостоверения",
		"свидетельство о рождении ребёнка",
		"страховой полис ОМС",
	}
	for _, s := range secret {
		if got := sentClass(s); got != store.ClassSecret {
			t.Errorf("%q → %q, а должно быть секретным", s, got)
		}
	}
}

// Свой или чужой считается по адресу, а не настройкой: человек не должен
// помнить, что он поменял base_url и тем самым открыл дверь наружу.
func TestEmbedderOwnForeign(t *testing.T) {
	own := []string{
		"http://127.0.0.1:1234/v1", "http://localhost:8080/v1",
		"http://embed:8080/v1", "http://192.168.88.10:11434/v1",
		"http://10.0.0.5/v1",
	}
	foreign := []string{
		"https://api.openai.com/v1", "https://api.jina.ai/v1",
		"http://8.8.8.8/v1",
	}
	for _, a := range own {
		if embed.New(a, "m", "").Outside {
			t.Errorf("%s посчитан чужим", a)
		}
	}
	for _, a := range foreign {
		if !embed.New(a, "m", "").Outside {
			t.Errorf("%s посчитан своим", a)
		}
	}
}

// Письмо, у которого класс «секретное», не должно доходить до облачной
// модели даже ради разметки спама: раньше разметка звала «активную» модель
// и ни про класс, ни про закрытость ящика не знала вовсе.
func TestMarkupSecretMessages(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	// одна модель, и она облачная
	if err := e.saveUserModels([]ModelConf{{Name: "облако", Type: "openai", BaseURL: "https://api.example.com/v1"}}); err != nil {
		t.Fatal(err)
	}
	roles := e.Roles()
	for i := range roles {
		if roles[i].Name == RoleFilter {
			roles[i].Model = "облако"
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	_ = st

	secret := &store.Message{FromAddr: "hr@work.example",
		Subject: "Скан паспорта для оформления", Snippet: "во вложении разворот"}
	if p := e.filterModel(secret); p != nil {
		t.Fatal("секретное письмо ушло облачной модели на разметку")
	}
	if !strings.Contains(secret.Reason, "наружу не отдаю") {
		t.Fatalf("причина отказа непонятная: %q", secret.Reason)
	}

	plain := &store.Message{FromAddr: "news@example.com",
		Subject: "Новости недели", Snippet: "что случилось за семь дней"}
	plain.Reason = ""
	if p := e.filterModel(plain); p == nil && plain.Reason != "" {
		t.Fatalf("обычное письмо тоже не размечается: %q", plain.Reason)
	}
}

// Класс умеет только расти: разбор может открыть, что в файле паспорт, но
// не может доказать, что его там нет. Пометка человека не трогается вовсе.
func TestClassOnlyGrows(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)

	e.raiseClass(store.ClassFile, 1, store.ClassSensitive, "слово «счёт»")
	if c, _ := st.ContentClass(store.ClassFile, 1); c != store.ClassSensitive {
		t.Fatalf("класс не поставился: %q", c)
	}
	e.raiseClass(store.ClassFile, 1, store.ClassSecret, "слово «паспорт»")
	if c, _ := st.ContentClass(store.ClassFile, 1); c != store.ClassSecret {
		t.Fatalf("класс не поднялся: %q", c)
	}
	e.raiseClass(store.ClassFile, 1, store.ClassPlain, "")
	if c, _ := st.ContentClass(store.ClassFile, 1); c != store.ClassSecret {
		t.Fatalf("класс опустился до %q — так нельзя", c)
	}

	// пометка человека главнее примет
	if err := st.SetContentClass(store.ClassFile, 2, store.ClassPlain, "вадим", ""); err != nil {
		t.Fatal(err)
	}
	e.raiseClass(store.ClassFile, 2, store.ClassSecret, "слово «паспорт»")
	if c, _ := st.ContentClass(store.ClassFile, 2); c != store.ClassPlain {
		t.Fatalf("пометку человека переписали на %q", c)
	}
}

// У картинки в промпте одна служебная инструкция: класс в журнале должен
// приходить от файла, иначе скан паспорта записывается как «обычное».
func TestClassInLogFromImage(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	prompt := "Ты видишь картинку из письма. Верни JSON без пояснений."
	if got := e.classOfCall(llmCall(prompt, ""), prompt); got != store.ClassPlain {
		t.Fatalf("без пометки класс берётся по тексту: ждали обычное, вышло %q", got)
	}
	if got := e.classOfCall(llmCall(prompt, store.ClassSecret), prompt); got != store.ClassSecret {
		t.Fatalf("класс от картинки потерялся: %q", got)
	}
}

func llmCall(prompt, class string) llm.Call {
	return llm.Call{Model: "тест", Prompt: prompt, Class: class}
}

// Предотвращённое считается отдельно от исходов работы: работа может
// закончиться удачей и всё равно содержать момент, где скилл не дал сделать
// глупость. Пока такое пряталось внутри «удалось».
func TestCountPrevented(t *testing.T) {
	_, st := newTestEngine(t, "тест", nil)
	if err := st.NoteSkillUse("Linux-сервер", 7); err != nil {
		t.Fatal(err)
	}
	if err := st.NoteSkillUse("MikroTik RouterOS", 7); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSkillSaved(7, "отказ по кругу «никогда»: rm -rf /"); err != nil {
		t.Fatal(err)
	}
	saved := st.SkillSaved("2000-01-01T00:00:00Z")
	if saved["Linux-сервер"] != 1 || saved["MikroTik RouterOS"] != 1 {
		t.Fatalf("предотвращённое не разошлось по скиллам работы: %v", saved)
	}
	// работа закончилась удачей — счёт предотвращённого от этого не пропадает
	if err := st.CloseSkillUses(7, "win"); err != nil {
		t.Fatal(err)
	}
	if got := st.SkillSaved("2000-01-01T00:00:00Z"); got["Linux-сервер"] != 1 {
		t.Fatalf("после удачного исхода счёт потерялся: %v", got)
	}
}

// Отказ отдать текст сетевому эмбеддеру должен быть виден там же, где
// видно ушедшее. Иначе запись без вектора выглядит потерей, а не решением.
func TestDenialEmbedderVisibleInLog(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	e.saveDenial("вектор", "секретное", "скан паспорта Иванова")

	rows, err := st.SentList(false, "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("отказ не попал в «Что ушло» — человек его не увидит")
	}
	r := rows[0]
	if r.OK {
		t.Fatal("отказ записан как удачная отправка")
	}
	if !strings.Contains(r.Err, "не отправлено") {
		t.Fatalf("причина непонятная: %q", r.Err)
	}
	if r.Class != store.ClassSecret {
		t.Fatalf("класс отказа: %q", r.Class)
	}
}

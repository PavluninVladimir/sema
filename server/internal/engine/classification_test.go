package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// cloudAndHome — две модели: одной человек доверил закрытое, другой
// нет. Половина проверок этого файла про разницу между ними.
func cloudAndHome(t *testing.T, e *Engine) {
	t.Helper()
	for _, m := range []ModelConf{
		{Name: "дом", Type: "openai", BaseURL: "http://llm:8080/v1", Model: "q", Trusted: true},
		{Name: "облако", Type: "openai", BaseURL: "https://api.example.com/v1", Model: "d"},
	} {
		if err := e.AddModel(m); err != nil {
			t.Fatal(err)
		}
	}
}

// 2.3. Пометка человека сильнее вычисленного класса — в обе стороны.
// Раньше дверь смотрела только на текст промпта: письмо, которое человек
// руками пометил секретным, уходило недоверенной модели, если в теме не
// нашлось ни одного слова-приметы. И наоборот — снятая человеком пометка
// ничего не значила, а значит снимать её было незачем.
func TestMarkHumanStrongerSigns(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	cloudAndHome(t, e)

	// в тексте примет нет вовсе, но человек сказал: это секретное
	if err := st.SetContentClass(store.ClassMsg, 7, store.ClassSecret, "Вадим", ""); err != nil {
		t.Fatal(err)
	}
	ctx := llm.WithSubject(context.Background(), store.ClassMsg, 7)
	r := e.BeforeModel(ctx, "облако", "Напомни, о чём договорились в четверг")
	if r.Allow {
		t.Fatal("письмо, помеченное человеком как секретное, ушло недоверенной модели")
	}
	if !strings.Contains(r.Reason, "пометка человека") {
		t.Fatalf("причина не называет пометку: %q", r.Reason)
	}

	// обратная сторона: человек снял пометку с письма, где слово-примета
	// стоит в цитате. Без этого обсудить такое письмо было бы нельзя
	if err := st.SetContentClass(store.ClassMsg, 8, store.ClassPlain, "Вадим", ""); err != nil {
		t.Fatal(err)
	}
	ctx = llm.WithSubject(context.Background(), store.ClassMsg, 8)
	if !e.BeforeModel(ctx, "облако", "они просят паспорт, это развод?").Allow {
		t.Fatal("снятая человеком пометка ничего не изменила")
	}

	// пометка самого Сёмы (by пустой) приметы не перебивает
	if err := st.SetContentClass(store.ClassMsg, 9, store.ClassPlain, "", "нет примет"); err != nil {
		t.Fatal(err)
	}
	ctx = llm.WithSubject(context.Background(), store.ClassMsg, 9)
	if e.BeforeModel(ctx, "облако", "вот скан паспорта").Allow {
		t.Fatal("класс, посчитанный Сёмой, перебил примету в самом промпте")
	}
}

// 2.4. Класс не посчитан — значит его не считали, а не «там ничего нет».
// На живых файлах приметы стояли дальше окна классификатора, класса у
// тринадцати из пятнадцати не было вовсе, и пустая строка проходила как
// «обычное»: система молчала, потому что не смотрела.
func TestUncountedClassCountsSensitive(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.SetSensitiveMode("ask"); err != nil {
		t.Fatal(err)
	}
	if give, why := e.outsideCut(true, "", "кто@то.рф"); give {
		_ = why
		t.Fatal("запись без класса ушла недоверенной модели как обычная")
	}
	// доверенной по-прежнему можно всё: непосчитанный класс не повод
	// перестать работать вовсе
	if give, _ := e.outsideCut(false, "", "кто@то.рф"); !give {
		t.Fatal("доверенной модели отказали из-за непосчитанного класса")
	}
	// в режиме «разрешать» чувствительное отдаётся, и пустой класс тоже:
	// правило одно и то же, а не отдельная строгость для пустоты
	if err := e.SetSensitiveMode("allow"); err != nil {
		t.Fatal(err)
	}
	if give, _ := e.outsideCut(true, "", "кто@то.рф"); !give {
		t.Fatal("в режиме «разрешать» пустой класс должен вести себя как чувствительный")
	}
}

// 3.2. Свой номер в реплике без слова-подсказки рядом. Ровно этот случай
// 8 сентября уехал облачной модели: приметы по словам его не видят, а
// сущности с классом в контексте не было вовсе.
func TestOwnValueCatchesNumberWithoutHint(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	cloudAndHome(t, e)

	foreign := "Оплати по реквизитам 500600700800, пожалуйста"
	if !e.BeforeModel(context.Background(), "облако", foreign).Allow {
		t.Fatal("чужой номер не должен ничего закрывать до того, как его внесли")
	}

	kind, err := e.AddOwnValue("5006 0070 0800")
	if err != nil {
		t.Fatal(err)
	}
	if kind == "" {
		t.Fatal("вид значения не назван — человек не поймёт, что Сёма записал")
	}
	r := e.BeforeModel(context.Background(), "облако", foreign)
	if r.Allow {
		t.Fatal("свой номер ушёл недоверенной модели")
	}
	if r.Class != store.ClassSecret || !strings.Contains(r.Reason, "своих значений") {
		t.Fatalf("решение не объясняет себя: класс %q, причина %q", r.Class, r.Reason)
	}
	// доверенной можно: список закрывает наружу, а не работу вообще
	if !e.BeforeModel(context.Background(), "дом", foreign).Allow {
		t.Fatal("свой номер не дошёл даже до доверенной модели")
	}

	// запись другими знаками — тот же номер
	if e.BeforeModel(context.Background(), "облако", "счёт 5006-0070-0800").Allow {
		t.Fatal("номер, записанный через дефисы, не узнан")
	}

	// самого номера в базе нет ни в каком виде: список — это хэши
	for _, z := range e.OwnValues() {
		if strings.Contains(z.Hash, "500600700800") || strings.Contains(z.Kind, "500600") {
			t.Fatal("номер лёг в хранилище открытым текстом")
		}
	}
	v, _ := e.Store().SecretValue(ownValuesSecret)
	if strings.Contains(v, "500600700800") {
		t.Fatal("номер виден в значении секрета")
	}

	// снятое значение перестаёт действовать
	if err := e.RemoveOwnValue(e.OwnValues()[0].Hash); err != nil {
		t.Fatal(err)
	}
	if !e.BeforeModel(context.Background(), "облако", foreign).Allow {
		t.Fatal("убранное значение всё ещё закрывает дверь")
	}
}

// Журнал считает класс той же меркой, что дверь. Разойтись им нельзя:
// 8 сентября решение принимал страж по сущностям, а класс в журнал писала
// другая функция по тексту — и настоящая утечка выглядела как ложная.
func TestLogAndGateCountSame(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	cloudAndHome(t, e)
	if _, err := e.AddOwnValue("5006 0070 0800"); err != nil {
		t.Fatal(err)
	}
	text := "переведи на 500600700800"
	r := e.BeforeModel(context.Background(), "облако", text)
	if got := e.PromptClass(text); got != r.Class {
		t.Fatalf("журнал пишет %q, дверь решала по %q", got, r.Class)
	}
}

// 3.5. Разметка спама смотрит на сохранённый класс. Раньше она считала
// приметы заново по теме и выжимке, минуя таблицу классов: пометка «это
// секретное» на этом пути не защищала вовсе.
func TestMarkupLooksOnSavedClass(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "облако", Type: "openai", BaseURL: "https://api.example.com/v1", Model: "d"}); err != nil {
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
	m := &store.Message{MessageID: "<1@x>", Account: "тест", FromAddr: "hr@work.example",
		Subject: "Договорённости по встрече", Snippet: "в четверг в двенадцать"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	// примет в теме и выжимке нет: по старому пути письмо ушло бы облаку
	if p := e.filterModel(m); p == nil {
		t.Fatalf("обычное письмо не размечается: %q", m.Reason)
	}
	if err := e.MarkClass(store.ClassMsg, m.ID, store.ClassSecret, "Вадим"); err != nil {
		t.Fatal(err)
	}
	m.Reason = ""
	if p := e.filterModel(m); p != nil {
		t.Fatal("письмо, помеченное человеком секретным, ушло облачной модели на разметку")
	}
	if !strings.Contains(m.Reason, "наружу не отдаю") {
		t.Fatalf("причина отказа непонятная: %q", m.Reason)
	}
}

// 4.1 и 4.3. Перечитывание вложений пересчитывает класс — и не трогает
// пометку человека. При приёме о фотографии знало только имя файла, а
// «IMG_2841.jpg» не говорит ничего: раньше перечитывание обновляло текст и
// вектор, а класс не трогало вовсе, и скан паспорта оставался «обычным»
// навсегда.
func TestRereadRecountsClass(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	dir := t.TempDir()
	// в тексте — машиночитаемая строка документа: её OCR вытаскивает даже
	// из мусорного распознавания, и она самая надёжная примета паспорта
	passport := "P<RUSIVANOV<<IVAN<<<<<<<<<<<<<<<<<<<<<<<<<<<\n4507123456RUS8501014M"

	file := func(name, text string) int64 {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		a := &store.Attachment{Filename: name, MIME: "text/plain", Path: path,
			Kind: store.AttKindDocument, Size: int64(len(text))}
		if err := st.InsertAttachment(a); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	fresh := file("IMG_2841.txt", passport)
	decided := file("IMG_2842.txt", passport)

	// класс при приёме посчитан по одному имени файла — он обычный
	if got := e.ClassOfFile(&store.Attachment{ID: fresh, Filename: "IMG_2841.txt"}); got != store.ClassPlain {
		t.Fatalf("до распознавания класс %q", got)
	}
	// человек посмотрел второй файл и сказал: это не секрет
	if err := e.MarkClass(store.ClassFile, decided, store.ClassPlain, "Вадим"); err != nil {
		t.Fatal(err)
	}

	if n, err := e.RescanAttachments(context.Background(), 10); err != nil || n != 2 {
		t.Fatalf("перечитано %d файлов, ошибка %v", n, err)
	}

	mark, ok := st.ClassMarkOf(store.ClassFile, fresh)
	if !ok || mark.Class != store.ClassSecret {
		t.Fatalf("после перечитывания класс остался %q", mark.Class)
	}
	// 4.2: причина хранится вместе с классом
	if mark.Why == "" {
		t.Fatal("класс есть, а почему — неизвестно: ответить «почему секретное» нечем")
	}
	// 4.3: решение человека пересчёт не переспорит
	if c, _ := st.ContentClass(store.ClassFile, decided); c != store.ClassPlain {
		t.Fatalf("пересчёт переписал пометку человека на %q", c)
	}
}

// 4.2. Причина у класса письма и файла хранится с первого же расчёта:
// раньше хранился только итог, и все выводы разбора приходилось получать
// пересчётом по коду, а не из данных.
func TestClassStoresFiredSign(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := &store.Message{MessageID: "<2@x>", Account: "тест", FromAddr: "hr@work.example",
		Subject: "Скан паспорта для оформления", Snippet: "во вложении разворот"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if got := e.ClassOfMessage(m); got != store.ClassSecret {
		t.Fatalf("класс письма %q", got)
	}
	mark, ok := st.ClassMarkOf(store.ClassMsg, m.ID)
	if !ok || !strings.Contains(mark.Why, "паспорт") {
		t.Fatalf("примета не сохранилась: %q", mark.Why)
	}
	// пометка человека затирает примету: причина теперь он сам
	if err := e.MarkClass(store.ClassMsg, m.ID, store.ClassPlain, "Вадим"); err != nil {
		t.Fatal(err)
	}
	mark, _ = st.ClassMarkOf(store.ClassMsg, m.ID)
	if mark.Why != "" || mark.By == "" {
		t.Fatalf("после пометки человека осталась чужая причина: by=%q why=%q", mark.By, mark.Why)
	}
}

// Письмо, которого ещё нет в базе, класс получает, но строку не заводит:
// разметка спама спрашивает класс до вставки, и строка с id = 0 склеила бы
// все неразмеченные письма в одну.
func TestClassMessagesWithoutNumberNotRemembered(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := &store.Message{FromAddr: "hr@work.example", Subject: "Скан паспорта", Snippet: "разворот"}
	if got := e.ClassOfMessage(m); got != store.ClassSecret {
		t.Fatalf("класс не посчитан: %q", got)
	}
	if _, has := st.ContentClass(store.ClassMsg, 0); has {
		t.Fatal("завелась строка класса для письма без номера")
	}
}

// 5.1. Дневной запрос по журналу честен: «недоверенной модели и
// секретное». По адресу считать нельзя — доверенная модель бывает
// удалённой и законно даёт outside = 1, а недоверенная стоит в соседнем
// контейнере и в счёт по адресу не попадает вовсе.
func TestCountSecretByDistrust(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.installUsage()
	for _, m := range []ModelConf{
		{Name: "своя-недоверенная", Type: "openai", BaseURL: "http://llm:8080/v1", Model: "q"},
		{Name: "удалённая-доверенная", Type: "openai", BaseURL: "https://api.example.com/v1", Model: "d", Trusted: true},
	} {
		if err := e.AddModel(m); err != nil {
			t.Fatal(err)
		}
	}
	e.recordSent(llm.Call{Model: "удалённая-доверенная", Consumer: "core:память", Prompt: "вот скан паспорта"})
	e.recordSent(llm.Call{Model: "своя-недоверенная", Consumer: "core:память", Prompt: "вот скан паспорта"})

	st2 := st.SentStats("2000-01-01T00:00:00Z")
	if st2.SecretUntrusted != 1 {
		t.Fatalf("секретного недоверенной модели насчитали %d, а ушло ровно одно", st2.SecretUntrusted)
	}
	// по старой мерке «наружу и секретное» вышло бы то же число, но
	// про другую отправку: проверяем, что считается именно недоверие
	rows, err := st.SentList(false, store.ClassSecret, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Model == "удалённая-доверенная" && r.Untrusted {
			t.Fatal("доверенная модель записана недоверенной")
		}
		if r.Model == "своя-недоверенная" && !r.Untrusted {
			t.Fatal("недоверенная модель в своей сети записана доверенной")
		}
	}
}

// seeingModel — провайдер, который умеет картинки. Настоящий нужен
// потому, что дверь оборачивает провайдера, а обёртка вокруг интерфейса
// продвигает только его методы.
type seeingModel struct {
	fakeProvider
	shown int
}

func (v *seeingModel) CompleteImage(ctx context.Context, prompt string, image []byte, mime string) (string, error) {
	v.shown++
	return v.fakeProvider.Complete(ctx, prompt)
}

// Дверь не должна молча отнимать у модели умение смотреть картинки.
// Обёртка вокруг `llm.Provider` продвигает только методы самого
// интерфейса, поэтому `p.(llm.Vision)` у неё не срабатывал — и Сёма
// записывал в настройках, что провайдер картинки не принимает.
func TestGateNotTakesImage(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	cloudAndHome(t, e)
	sees := &seeingModel{fakeProvider: fakeProvider{answer: `{"text":"чек на 200 рублей"}`}}
	blind := &fakeProvider{answer: "ок"}
	e.setProvider("дом", sees)
	e.setProvider("облако", blind)

	out, can, err := llm.SeeImage(context.Background(), e.provider("дом"), "что на картинке", []byte{1, 2}, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !can {
		t.Fatal("дверь спрятала умение модели смотреть картинки")
	}
	if sees.shown != 1 || !strings.Contains(out, "чек") {
		t.Fatalf("картинка до модели не дошла: показов %d, ответ %q", sees.shown, out)
	}
	// модель картинок не умеет — это «не умеет», а не сбой
	if _, can, err := llm.SeeImage(context.Background(), e.provider("облако"), "что там", []byte{1}, "image/png"); can || err != nil {
		t.Fatalf("слепая модель за дверью: умеет=%v err=%v", can, err)
	}
	// и дверь всё ещё дверь: секретное картинкой наружу не уходит
	seesCloud := &seeingModel{fakeProvider: fakeProvider{answer: "ок"}}
	e.setProvider("облако", seesCloud)
	if _, _, err := llm.SeeImage(context.Background(), e.provider("облако"),
		"на картинке скан паспорта", []byte{1}, "image/png"); err == nil {
		t.Fatal("секретный текст ушёл недоверенной модели вместе с картинкой")
	}
	if seesCloud.shown != 0 {
		t.Fatal("картинка всё-таки уехала")
	}
}

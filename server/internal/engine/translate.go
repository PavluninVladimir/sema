package engine

// Перевод писем. Половина рассылок и все письма от зарубежных сервисов
// приходят не по-русски, и человек читает их через браузерный переводчик —
// то есть отдаёт письмо на чужой сервер. Сёма умеет спросить свою модель.
//
// Три правила, из которых сделано всё остальное.
//
// Подлинник не трогаем: перевод лежит своей записью, письмо остаётся тем,
// что прислали. Иначе поиск, память и разбор начали бы работать по пересказу.
//
// Класс письма сильнее удобства: секретное письмо уходит переводить только
// доверенной модели, а нет такой — не уходит вовсе. Перевод — это отправка
// текста письма наружу, ничем не отличающаяся от любой другой.
//
// Язык называет человек, а не код. Он же решает, переводить ли вообще:
// каждый перевод — вызов модели, а на Raspberry Pi это минуты и деньги.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/htmlsafe"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	// WorkTranslate — вид работы очереди: одно письмо, один вызов модели.
	WorkTranslate = "msg_translate"

	// RoleTranslate — своя роль: переводит обычно не та модель, что судит
	// письма. Человек может поставить сюда локальную — тогда перевод
	// бесплатный и не уходит наружу вовсе.
	RoleTranslate = "Перевод"

	translateOnKey    = "mail.translate"
	translateToKey    = "mail.translate_to"
	translateLangsKey = "mail.translate_langs"
	translateCharsKey = "mail.translate_chars"

	translateLangsSeed = "Русский, Английский, Немецкий, Французский, Испанский, Итальянский, Португальский, Польский, Турецкий, Китайский, Японский"
)

// TranslateOn — переводит ли Сёма письма сам. По умолчанию нет: перевод
// стоит вызова модели на каждое письмо, и включает его человек.
func (e *Engine) TranslateOn() bool { return e.store.Setting(translateOnKey, "0") == "1" }

func (e *Engine) SetTranslateOn(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting(translateOnKey, v)
}

// TranslateTo — на какой язык переводить.
func (e *Engine) TranslateTo() string {
	return strings.TrimSpace(e.store.Setting(translateToKey, "Русский"))
}

func (e *Engine) SetTranslateTo(lang string) error {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return fmt.Errorf("язык не назван")
	}
	// новый язык дописывается в справочник: в следующий раз он уже
	// выбирается из списка, а не набирается заново
	e.rememberLang(lang)
	return e.store.SetSetting(translateToKey, lang)
}

// TranslateLangs — справочник языков для выбора. Список живёт в настройках,
// а не в коде: человек добавляет свой язык и он остаётся.
func (e *Engine) TranslateLangs() []string {
	raw := e.store.Setting(translateLangsKey, translateLangsSeed)
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (e *Engine) rememberLang(lang string) {
	for _, l := range e.TranslateLangs() {
		if strings.EqualFold(l, lang) {
			return
		}
	}
	_ = e.store.SetSetting(translateLangsKey, strings.Join(append(e.TranslateLangs(), lang), ", "))
}

// TranslateChars — сколько знаков письма отдаём модели. Договор на сорок
// страниц перевести целиком нельзя: у ответа модели свой предел
// (`models.max_output`), и обрезанный на середине перевод хуже честного
// «показано начало».
func (e *Engine) TranslateChars() int {
	v, err := strconv.Atoi(e.store.Setting(translateCharsKey, "4000"))
	if err != nil || v < 200 {
		return 4000
	}
	return v
}

func (e *Engine) SetTranslateChars(n int) error {
	if n < 200 {
		return fmt.Errorf("меньше 200 знаков переводить незачем")
	}
	return e.store.SetSetting(translateCharsKey, strconv.Itoa(n))
}

// EnqueueTranslate — поставить письмо в очередь на перевод. Зовётся там же,
// где выпускается `message.added`.
func (e *Engine) EnqueueTranslate(id int64) {
	if id == 0 || !e.TranslateOn() {
		return
	}
	_ = e.store.Enqueue(WorkTranslate, strconv.FormatInt(id, 10), "", 6)
}

// MessageTranslation — перевод письма на нынешний целевой язык, для экрана.
// Язык сменили — прежние переводы не подходят, и экран их не покажет.
func (e *Engine) MessageTranslation(id int64) (store.Translation, bool) {
	return e.store.Translation(id, e.TranslateTo())
}

// translatePrompt — короткий вопрос и ответ одним JSON. Разъяснений не
// просим: они съедают предел ответа, который нужен самому переводу.
const translatePrompt = "Переведи письмо и ответь ТОЛЬКО JSON, без пояснений и без ограждений:\n" +
	`{"lang": "язык оригинала по-русски", "same": false, "subject": "перевод темы", "text": "перевод письма"}` + "\n" +
	"same — true, если письмо уже на языке %[1]s; тогда subject и text оставь пустыми.\n" +
	"Переводи на %[1]s. Смысл важнее красоты: суммы, даты, номера счетов и адреса переноси как есть.\n" +
	"Разметку и подписи рассылок не пересказывай — переводи то, что есть.\n\n"

type translateReply struct {
	Lang    string `json:"lang"`
	Same    bool   `json:"same"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

// handleTranslate — одна работа очереди: перевести одно письмо.
func (e *Engine) handleTranslate(ctx context.Context, w *store.WorkItem) error {
	if !e.TranslateOn() {
		return nil // человек выключил перевод: работа сделана тем, что не сделана
	}
	id, err := strconv.ParseInt(w.Ref, 10, 64)
	if err != nil {
		return fmt.Errorf("непонятная ссылка %q", w.Ref)
	}
	m, err := e.store.Message(id)
	if err != nil {
		return nil // письма больше нет: переводить нечего
	}
	lang := e.TranslateTo()
	if _, has := e.store.Translation(id, lang); has {
		return nil
	}
	bodies, err := e.store.Bodies([]int64{id})
	if err != nil {
		return err
	}
	inHTML := strings.TrimSpace(bodies[id].HTML)
	text := strings.TrimSpace(bodies[id].Text)
	if inHTML == "" && text == "" {
		// тело письма приезжает отдельной работой; подождём его, а не
		// объявим письмо непереводимым
		return postpone(2*time.Hour, "тело письма ещё не скачано")
	}
	if until, resting := e.roleCooldown(RoleTranslate); resting && !e.CloudHelpOn() {
		return postpone(time.Until(until)+time.Minute, "роль «Перевод» на передышке")
	}
	cloud, _ := e.needsCloudHelp(WorkTranslate)

	// закрытый ящик и класс «секретное» — два разных запрета, одна дверь
	closed := e.mailboxClosedForCloud(e.accountOr(m.Account)) || e.ClassOfMessage(m) == store.ClassSecret

	// письмо с вёрсткой переводим по местам: слова меняются, письмо
	// остаётся письмом. Текстовое тело — запасной путь для писем без HTML
	if inHTML != "" {
		err := e.translateWholeMessage(ctx, m, inHTML, lang, cloud, closed)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errNoTrusted):
			return postpone(6*time.Hour, "закрытое письмо, а доверенной модели нет")
		case isBusyErr(err):
			return postpone(10*time.Minute, clip(err.Error(), 120))
		}
		return err
	}
	answer, _, err := e.askRoleClosed(ctx, RoleTranslate, e.translateQuestion(m, text, lang), cloud, closed)
	if err != nil {
		switch {
		case errors.Is(err, errNoTrusted):
			return postpone(6*time.Hour, "закрытое письмо, а доверенной модели нет")
		case isBusyErr(err):
			return postpone(10*time.Minute, clip(err.Error(), 120))
		}
		return err
	}
	var o translateReply
	if err := json.Unmarshal([]byte(extractJSON(answer)), &o); err != nil {
		log.Printf("перевод письма %d (%q): ответ модели не разобран — %s", id, m.Subject, clip(answer, 120))
		return nil
	}
	t := store.Translation{MessageID: id, Lang: lang, FromLang: clip(strings.TrimSpace(o.Lang), 40)}
	if o.Same || strings.TrimSpace(o.Text) == "" {
		// пустая запись — это «смотрел, переводить не нужно»: она держит
		// письмо вне следующей выборки, и второй раз к модели мы не пойдём
		return e.store.SaveTranslation(t)
	}
	t.Subject = clip(strings.TrimSpace(o.Subject), 400)
	t.Text = strings.TrimSpace(o.Text)
	if len([]rune(text)) > e.TranslateChars() {
		t.Text += "\n\n⟨переведено начало письма — " + strconv.Itoa(e.TranslateChars()) + " знаков; остальное смотрите в подлиннике⟩"
	}
	if err := e.store.SaveTranslation(t); err != nil {
		return err
	}
	e.Changed("msg:" + strconv.FormatInt(id, 10))
	return nil
}

// translateQuestion — что показываем модели: тема, отправитель и текст.
func (e *Engine) translateQuestion(m *store.Message, text, lang string) string {
	var b strings.Builder
	fmt.Fprintf(&b, translatePrompt, lang)
	if m.FromAddr != "" {
		b.WriteString("От: " + m.FromAddr + "\n")
	}
	if m.Subject != "" {
		b.WriteString("Тема: " + m.Subject + "\n")
	}
	b.WriteString("---\n" + clip(text, e.TranslateChars()) + "\n---\n")
	return b.String()
}

// enqueueTranslateBacklog — догнать письма, пришедшие до включения перевода
// или пока модель молчала. Зовётся «Сборщиком очереди»: очередь не
// единственный источник правды.
func (e *Engine) EnqueueTranslateBacklog(limit int) int {
	if !e.TranslateOn() {
		return 0
	}
	list, err := e.store.MessagesToTranslate(e.TranslateTo(), limit)
	if err != nil {
		return 0
	}
	n := 0
	for _, id := range list {
		ref := strconv.FormatInt(id, 10)
		if e.store.HasWork(WorkTranslate, ref) {
			continue
		}
		if err := e.store.Enqueue(WorkTranslate, ref, "", 6); err == nil {
			n++
		}
	}
	return n
}

// ---- перевод письма по местам ----

// translateLines — сколько кусочков текста отдаём модели за один раз.
// Рассылка на сорок ячеек таблицы — это сотни коротких строк, и вся пачка
// в один запрос не влезет; а дробить слишком мелко значит платить за
// повторный контекст.
const translateLines = 120

// translateByPlacesPrompt — просим массив той же длины и в том же порядке.
// Любой другой ответ мы не примем: сдвиг на одну строку переставит слова
// по чужим местам, и письмо станет бессмыслицей, выглядящей осмысленно.
const translateByPlacesPrompt = "Переведи каждую строку на %[1]s. Ответь ТОЛЬКО JSON-массивом строк той же длины и в том же порядке, без пояснений и без ограждений.\n" +
	"Строку, которая уже на %[1]s, оставь как есть. Суммы, даты, номера счетов, адреса и ссылки не трогай.\n" +
	"Ничего не объединяй и не разбивай: сколько строк пришло, столько и верни.\n\n"

func (e *Engine) translateWholeMessage(ctx context.Context, m *store.Message,
	inHTML, lang string, cloud, closed bool) error {

	line := htmlsafe.Lines(inHTML)
	if len(line) == 0 {
		return e.store.SaveTranslation(store.Translation{MessageID: m.ID, Lang: lang})
	}
	transfers := make([]string, len(line))
	copy(transfers, line) // не переведённое остаётся подлинником
	translated := 0
	for start := 0; start < len(line); start += translateLines {
		end := start + translateLines
		if end > len(line) {
			end = len(line)
		}
		chunk := line[start:end]
		got, err := e.translateBatch(ctx, chunk, lang, cloud, closed)
		if err != nil {
			if translated == 0 {
				return err // не смогли вовсе — пусть очередь повторит
			}
			log.Printf("перевод письма %d: пачка с %d не перевелась: %v", m.ID, start, err)
			break
		}
		if len(got) != len(chunk) {
			// сдвиг на одну строку переставит слова по чужим местам, и
			// письмо станет бессмыслицей, выглядящей осмысленно
			log.Printf("перевод письма %d: модель вернула %d строк вместо %d — пачка пропущена",
				m.ID, len(got), len(chunk))
			continue
		}
		copy(transfers[start:end], got)
		translated += len(got)
	}
	if translated == 0 {
		return fmt.Errorf("модель не перевела ни одной строки")
	}
	i := htmlsafe.Translate(inHTML, transfers, false)
	t := store.Translation{MessageID: m.ID, Lang: lang, HTML: i.HTML}
	// тема — отдельной строкой: она в письме не лежит
	if m.Subject != "" {
		if got, err := e.translateBatch(ctx, []string{m.Subject}, lang, cloud, closed); err == nil && len(got) == 1 {
			t.Subject = clip(got[0], 400)
		}
	}
	if err := e.store.SaveTranslation(t); err != nil {
		return err
	}
	e.Changed("msg:" + strconv.FormatInt(m.ID, 10))
	log.Printf("переведено письмо %d (%q): строк %d из %d", m.ID, clip(m.Subject, 50), translated, len(line))
	return nil
}

// translateBatch — один вопрос модели на пачку строк.
func (e *Engine) translateBatch(ctx context.Context, line []string, lang string,
	cloud, closed bool) ([]string, error) {

	batch, err := json.Marshal(line)
	if err != nil {
		return nil, err
	}
	question := fmt.Sprintf(translateByPlacesPrompt, lang) + string(batch)
	reply, _, err := e.askRoleClosed(ctx, RoleTranslate, question, cloud, closed)
	if err != nil {
		return nil, err
	}
	var got []string
	if err := json.Unmarshal([]byte(arrayFromReply(reply)), &got); err != nil {
		return nil, fmt.Errorf("ответ модели не разобран: %s", clip(reply, 120))
	}
	return got, nil
}

// arrayFromReply — первый [...] из ответа: модели любят обрамлять JSON
// пояснениями и ```-ограждениями.
func arrayFromReply(s string) string {
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

package engine

// Голос на поддельном OpenAI: сервер записывает, что у него спросили
// (модель, язык, размер записи), и отвечает как настоящий. Так видно, что
// именно ушло облачной модели и ушло ли вообще, — а не только то, что
// вызов «не упал».

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// speechOgg — минимальный Ogg Opus нужной длины: страница с OpusHead и
// страница с позицией. Звука в нём нет, но контейнер настоящий, и длительность
// читается из него, как из голосового Telegram.
func speechOgg(seconds float64) []byte {
	page := func(granule int64, payload []byte) []byte {
		h := make([]byte, 27)
		copy(h, "OggS")
		binary.LittleEndian.PutUint64(h[6:], uint64(granule))
		h[26] = 1
		return append(append(h, byte(len(payload))), payload...)
	}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8], head[9] = 1, 1
	binary.LittleEndian.PutUint16(head[10:], 312)
	return append(page(0, head), page(int64(seconds*48000)+312, []byte("тишина"))...)
}

// ears — поддельный /audio/transcriptions.
type ears struct {
	mu     sync.Mutex
	hits   int
	models []string
	langs  []string
	sizes  []int
	auth   []string
	text   string // что «услышал»
	status int    // не ноль — отказ этим кодом
	url    string
}

func newEars(t *testing.T, text string) *ears {
	t.Helper()
	f := &ears{text: text}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("путь: %s", r.URL.Path)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits++
		if err := r.ParseMultipartForm(8 << 20); err == nil {
			f.models = append(f.models, r.FormValue("model"))
			f.langs = append(f.langs, r.FormValue("language"))
			if file, _, err := r.FormFile("file"); err == nil {
				buf := make([]byte, 8<<20)
				n, _ := file.Read(buf)
				f.sizes = append(f.sizes, n)
			}
		}
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if f.status != 0 {
			http.Error(w, `{"error":{"message":"сбой у провайдера"}}`, f.status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"text": f.text})
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func (f *ears) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

// connectEars подключает модель со слухом: в описании она «у OpenAI» (чужой
// адрес — облако, деньги считаются), а ходит провайдер в поддельный сервер.
// trusted — своя модель: ей можно всё.
func connectEars(t *testing.T, e *Engine, f *ears, name, model string, trusted bool, price string) {
	t.Helper()
	conf := ModelConf{Name: name, Type: "openai", BaseURL: "https://api.openai.com/v1", Model: model,
		Trusted: trusted, Price: price, Hears: hearsByName(model)}
	if trusted {
		conf.BaseURL = "http://127.0.0.1:9/v1"
	}
	if err := e.saveUserModels(append(e.userModels(), conf)); err != nil {
		t.Fatal(err)
	}
	p, err := llm.Build(name, config.Provider{Type: "openai", BaseURL: f.url, Model: model, APIKey: "ключ"})
	if err != nil {
		t.Fatal(err)
	}
	e.setProvider(name, p)
}

func setEarsRole(t *testing.T, e *Engine, name string) {
	t.Helper()
	if err := e.SetRoleField(RoleTranscribe, "model", name); err != nil {
		t.Fatalf("роль «%s»: %v", RoleTranscribe, err)
	}
}

// usageAll — все записи журнала расхода: по потребителям, как их отдаёт хранилище.
func usageAll(st *store.Store) []*store.UsageRow {
	var out []*store.UsageRow
	for _, c := range st.UsageConsumers() {
		rows, _ := st.UsageOfConsumer(c, 50)
		out = append(out, rows...)
	}
	return out
}

func TestParsePerMinute(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
		cur  string
		ok   bool
	}{
		{"$0.003 / мин", 0.003, "$", true},
		{"$0.003/min", 0.003, "$", true},
		{"$0.006 / Мин", 0.006, "$", true},
		{"₽0.3 / мин", 0.3, "₽", true},
		{"$0.55 / 1M", 0, "", false},
		{"$0.22 / $0.66 / $0.007 / 1M | $0.44 / $1.32 / $0.014 / 1M | пн-пт 01:00-04:00 UTC", 0, "", false},
		{"", 0, "", false},
	} {
		v, cur, ok := parsePerMinute(tc.in)
		if ok != tc.ok || v != tc.want || cur != tc.cur {
			t.Errorf("%q: %v %q %v, ждали %v %q %v", tc.in, v, cur, ok, tc.want, tc.cur, tc.ok)
		}
	}
}

// На роль речи ставится только модель со слухом: остальным на ней нечего
// делать, они отвечали бы ошибкой на первом же голосовом.
func TestRoleTakesOnlyHearingModels(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	f := newEars(t, "текст")
	if err := e.AddModel(ModelConf{Name: "болтун", Type: "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-6-luna"}); err != nil {
		t.Fatal(err)
	}
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "")

	err := e.SetRoleField(RoleTranscribe, "model", "болтун")
	if err == nil || !strings.Contains(err.Error(), "не слышит речь") {
		t.Fatalf("чатовая модель поставлена на роль речи: %v", err)
	}
	if err := e.SetRoleField(RoleTranscribe, "spare", "болтун"); err == nil {
		t.Fatal("чатовая модель поставлена запасной")
	}
	if got := e.Role(RoleTranscribe).Model; got == "болтун" {
		t.Fatal("отказ не помешал записи")
	}
	setEarsRole(t, e, "слух")
	if got := e.Role(RoleTranscribe).Model; got != "слух" {
		t.Fatalf("роль: %q", got)
	}
	if err := e.SetRoleField(RoleTranscribe, "model", "нет"); err != nil {
		t.Fatalf("роль речи нельзя выключить: %v", err)
	}
	// умение по названию не угадать — человек отмечает сам, и тогда можно
	if err := e.AddModel(ModelConf{Name: "мой-сервер", Type: "openai", BaseURL: "http://192.168.1.5:9000/v1", Model: "large-v3"}); err != nil {
		t.Fatal(err)
	}
	if e.SetRoleField(RoleTranscribe, "model", "мой-сервер") == nil {
		t.Fatal("модель без признаков слуха принята")
	}
	if err := e.SetModelHears("мой-сервер", true); err != nil {
		t.Fatal(err)
	}
	if err := e.SetRoleField(RoleTranscribe, "model", "мой-сервер"); err != nil {
		t.Fatalf("отмеченная человеком модель не принята: %v", err)
	}
	// у неречевого вида подключения слуха нет и отметка его не даст
	if err := e.AddModel(ModelConf{Name: "клод", Type: "anthropic", BaseURL: "https://api.anthropic.com", Model: "whisper-x", Hears: true}); err != nil {
		t.Fatal(err)
	}
	if e.ModelHears("клод") {
		t.Fatal("у Anthropic появился слух")
	}
}

// Подключение провайдера тянет список моделей, и речевые узнаются по
// названию сами: gpt-4o-mini-transcribe и whisper-1 — со слухом, остальные нет.
func TestHearingIsRecognizedByName(t *testing.T) {
	for _, m := range []string{"gpt-4o-transcribe", "gpt-4o-mini-transcribe", "whisper-1", "openai/whisper-large-v3", "faster-whisper", "stt-1"} {
		if !hearsByName(m) {
			t.Errorf("%s не узнана слышащей", m)
		}
	}
	for _, m := range []string{"gpt-6-luna", "gpt-4o", "gpt-4o-mini-tts", "text-embedding-3-small", "settings-model"} {
		if hearsByName(m) {
			t.Errorf("%s ошибочно слышит", m)
		}
	}
	e, _ := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "подключённая", Type: "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini-transcribe"}); err != nil {
		t.Fatal(err)
	}
	if !e.ModelHears("подключённая") {
		t.Fatal("AddModel не отметил слух по названию")
	}
	for _, m := range e.Models() {
		if m.Name == "подключённая" && (!m.Hears || !strings.Contains(m.Meta, "слышит речь")) {
			t.Fatalf("экран не говорит, что модель слышит: %+v", m)
		}
	}
}

// Пока человек не выбрал, роль берёт самое дешёвое из умеющего: свою
// модель, а нет её — gpt-4o-mini-transcribe. Остальные облачные сами не
// выбираются: гадать между ними за чужие деньги нельзя.
func TestHearingRoleDefault(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	f := newEars(t, "")
	if got := e.Role(RoleTranscribe).Model; got != "нет" {
		t.Fatalf("без моделей со слухом роль на %q", got)
	}
	connectEars(t, e, f, "дорогая", "gpt-4o-transcribe", false, "")
	if got := e.Role(RoleTranscribe).Model; got != "нет" {
		t.Fatalf("роль сама заняла более дорогую модель: %q", got)
	}
	connectEars(t, e, f, "дешёвая", "gpt-4o-mini-transcribe", false, "")
	if got := e.Role(RoleTranscribe).Model; got != "дешёвая" {
		t.Fatalf("умолчание — не gpt-4o-mini-transcribe: %q", got)
	}
	connectEars(t, e, f, "своя", "whisper-large-v3", true, "")
	if got := e.Role(RoleTranscribe).Model; got != "своя" {
		t.Fatalf("своя модель бесплатнее и никуда не уходит, а выбрана %q", got)
	}
	// выбор человека сильнее умолчания
	setEarsRole(t, e, "дорогая")
	if got := e.Role(RoleTranscribe).Model; got != "дорогая" {
		t.Fatalf("выбор человека затёрт умолчанием: %q", got)
	}
}

// Запрос к OpenAI: модель, язык из настройки и сама запись; в журнал расхода
// ложатся секунды звука и деньги по минутной цене, в «Что ушло» — «аудио N с»,
// а не байты.
func TestHearSendsAudioAndRecordsUsage(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.installUsage()
	f := newEars(t, "какие письма пришли сегодня")
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "$0.003 / мин")
	setEarsRole(t, e, "слух")

	audio := speechOgg(30)
	text, model, err := e.Hear(context.Background(), VoiceIn{Audio: audio, MIME: "audio/ogg", Own: true, Consumer: "core:telegram"})
	if err != nil || text != "какие письма пришли сегодня" || model != "слух" {
		t.Fatalf("текст %q, модель %q, ошибка %v", text, model, err)
	}
	f.mu.Lock()
	if f.hits != 1 || f.models[0] != "gpt-4o-mini-transcribe" || f.langs[0] != "ru" || f.sizes[0] != len(audio) || f.auth[0] != "Bearer ключ" {
		t.Errorf("запрос: %d раз, модель %v, язык %v, %v байт, ключ %v", f.hits, f.models, f.langs, f.sizes, f.auth)
	}
	f.mu.Unlock()

	// язык — настройка: пустой значит «пусть модель определит сама», а не «ru»
	if err := e.SetSpeechLanguage(""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Hear(context.Background(), VoiceIn{Audio: audio, MIME: "audio/ogg", Own: true, Consumer: "core:telegram"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.langs[1] != "" {
		t.Errorf("пустой язык всё равно отправлен: %q", f.langs[1])
	}
	f.mu.Unlock()

	// расход: тридцать секунд по $0.003 за минуту
	rows := usageAll(st)
	var first *store.UsageRow
	for _, r := range rows {
		if r.Role == "transcribe" {
			first = r
		}
	}
	if first == nil || first.Consumer != "core:telegram" || first.Model != "слух" || !first.OK {
		t.Fatalf("запись расхода: %+v", first)
	}
	if first.Cost < 0.00149 || first.Cost > 0.00151 || first.Currency != "$" || !strings.Contains(first.Note, "аудио 30 с") {
		t.Fatalf("деньги: %v %s, заметка %q", first.Cost, first.Currency, first.Note)
	}
	// журнал отправленного: секунды, а не байты; наружу, недоверенной; класс не «обычное»
	sent, err := st.SentList(false, "", "", 10)
	if err != nil || len(sent) == 0 {
		t.Fatalf("журнал отправленного: %d строк, %v", len(sent), err)
	}
	var row *store.SentRow
	for _, r := range sent {
		if r.Kind == "transcribe" {
			row = r
		}
	}
	if row == nil || row.Preview != "аудио 30 с" || !row.Outside || !row.Untrusted || row.Class != store.ClassSensitive {
		t.Fatalf("«Что ушло»: %+v", row)
	}
	if strings.Contains(row.Prompt, "OggS") {
		t.Fatal("в журнал отправленного попали байты записи")
	}
}

// Цена не задана — деньги не выдуманы, а в заметке сказано словами.
func TestAudioWithoutPriceSaysSo(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.installUsage()
	f := newEars(t, "раз")
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "")
	setEarsRole(t, e, "слух")
	if _, _, err := e.Hear(context.Background(), VoiceIn{Audio: speechOgg(10), MIME: "audio/ogg", Own: true, Consumer: "core:files"}); err != nil {
		t.Fatal(err)
	}
	rows := usageAll(st)
	for _, r := range rows {
		if r.Role == "transcribe" {
			if r.Cost != 0 || !strings.Contains(r.Note, "цена за минуту не задана") {
				t.Fatalf("без цены: %v, %q", r.Cost, r.Note)
			}
			return
		}
	}
	t.Fatal("записи расхода нет")
}

// Цена за минуту — не цена за миллион токенов: по токенам такая модель не
// считается, иначе «$0.003 / мин» превратилось бы в $0.003 за миллион.
func TestPerMinutePriceIsNotPerMillion(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	f := newEars(t, "")
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "$0.003 / мин")
	if cost, _ := e.costOfUsage("слух", llm.Usage{In: 1_000_000, Out: 1_000_000, Exact: true}); cost != 0 {
		t.Fatalf("цена за минуту посчитана как за миллион токенов: %v", cost)
	}
	cost, cur, priced := e.costOfAudio("слух", llm.Usage{AudioSec: 90})
	if !priced || cur != "$" || cost < 0.00449 || cost > 0.00451 {
		t.Fatalf("полторы минуты: %v %s %v", cost, cur, priced)
	}
	facts := ""
	for _, m := range e.Models() {
		if m.Name == "слух" {
			for _, f := range m.Facts {
				facts += f.Label + "=" + f.Value + ";"
			}
		}
	}
	if !strings.Contains(facts, "Цена=$0.003 / мин звука") || strings.Contains(facts, "1M") {
		t.Fatalf("факты модели: %s", facts)
	}
	if got := e.PriceShort("слух"); got != "$0.003 / мин" {
		t.Fatalf("кратко: %q", got)
	}
}

// speech.outside: облачной модели — только голос владельца (own), любой
// (all) или никакой (never). Доверенной — всегда. Отказ не доходит до сети и
// не красит расход: это решение, а не сбой.
func TestSpeechOutsideDecidesWhoseVoiceLeaves(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.installUsage()
	cloud := newEars(t, "облако услышало")
	own := newEars(t, "свой услышал")
	connectEars(t, e, cloud, "облако", "gpt-4o-mini-transcribe", false, "$0.003 / мин")
	connectEars(t, e, own, "свой", "whisper-large-v3", true, "")
	setEarsRole(t, e, "облако")
	audio := speechOgg(5)
	hear := func(ownVoice bool) (string, error) {
		text, _, err := e.Hear(context.Background(), VoiceIn{Audio: audio, MIME: "audio/ogg", Own: ownVoice, Consumer: "core:telegram"})
		return text, err
	}

	// own (по умолчанию): свой голос уходит, чужой — нет
	if e.SpeechOutside() != "own" {
		t.Fatalf("умолчание: %q", e.SpeechOutside())
	}
	if text, err := hear(true); err != nil || text != "облако услышало" {
		t.Fatalf("свой голос: %q, %v", text, err)
	}
	if _, err := hear(false); err == nil || !strings.Contains(err.Error(), "из чужих чатов не уходят облачной модели") {
		t.Fatalf("чужой голос при own: %v", err)
	}
	if cloud.count() != 1 {
		t.Fatalf("облако получило %d записей, а должно одну — чужая уйти не вправе", cloud.count())
	}
	// отказ по классу — не сбой: в расходе нет красной строки, роль не отдыхает
	rows := usageAll(st)
	for _, r := range rows {
		if !r.OK {
			t.Fatalf("отказ по классу записан сбоем: %+v", r)
		}
	}
	for i := 0; i < 4; i++ {
		_, _ = hear(false)
	}
	if _, resting := e.roleCooldown(RoleTranscribe); resting {
		t.Fatal("пять отказов по классу отправили роль на передышку")
	}

	// all: любые
	if err := e.SetSpeechOutside("all"); err != nil {
		t.Fatal(err)
	}
	if text, err := hear(false); err != nil || text != "облако услышало" {
		t.Fatalf("all: %q, %v", text, err)
	}
	// never: никакие, даже свои
	if err := e.SetSpeechOutside("never"); err != nil {
		t.Fatal(err)
	}
	before := cloud.count()
	if _, err := hear(true); err == nil || !strings.Contains(err.Error(), "не уходят облачной модели") {
		t.Fatalf("never: %v", err)
	}
	if cloud.count() != before {
		t.Fatal("при never запись всё равно ушла")
	}
	// доверенной — всегда: голос дальше своей машины не идёт
	setEarsRole(t, e, "свой")
	if text, err := hear(false); err != nil || text != "свой услышал" {
		t.Fatalf("доверенная модель при never: %q, %v", text, err)
	}
	if err := e.SetSpeechOutside("что-то"); err == nil {
		t.Fatal("принято несуществующее значение")
	}
}

// Запасная модель роли — тоже под политикой: облачная основная отказала по
// классу — запись достаётся доверенной запасной, а не пропадает.
func TestSpareTrustedModelHearsForeignVoice(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	cloud := newEars(t, "облако")
	home := newEars(t, "запасной услышал")
	connectEars(t, e, cloud, "облако", "gpt-4o-mini-transcribe", false, "")
	connectEars(t, e, home, "дома", "whisper-large-v3", true, "")
	setEarsRole(t, e, "облако")
	if err := e.SetRoleField(RoleTranscribe, "spare", "дома"); err != nil {
		t.Fatal(err)
	}
	text, model, err := e.Hear(context.Background(), VoiceIn{Audio: speechOgg(3), MIME: "audio/ogg", Consumer: "core:max"})
	if err != nil || text != "запасной услышал" || model != "дома" {
		t.Fatalf("%q от %q, %v", text, model, err)
	}
	if cloud.count() != 0 {
		t.Fatal("чужое голосовое ушло облаку, хотя есть своя запасная")
	}
}

// Дверь оборачивает распознавание: модель из provider() — не «голый»
// провайдер. Чужое голосовое без метки «своё» дверь не пускает, даже если
// вызывающий про политику забыл.
func TestGateWrapsTranscribe(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.installUsage()
	f := newEars(t, "услышал")
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "")
	p := e.provider("слух")
	if _, ok := p.(llm.Transcriber); !ok {
		t.Fatal("у двери нет Transcribe: llm.Hear не увидит слух у обёртки")
	}
	audio := speechOgg(2)
	if _, ok, err := llm.Hear(context.Background(), p, audio, "audio/ogg", "ru"); !ok || err == nil ||
		!strings.Contains(err.Error(), "Сёма не отдал голосовое недоверенной модели") {
		t.Fatalf("чужое голосовое прошло дверь: ok=%v err=%v", ok, err)
	}
	if f.count() != 0 {
		t.Fatal("запись дошла до провайдера мимо двери")
	}
	// отказ виден в журнале расхода, как отказ по классу у текста
	rows := usageAll(st)
	if len(rows) == 0 || rows[0].Role != "gate" || rows[0].OK {
		t.Fatalf("отказ двери не записан: %+v", rows)
	}
	// свой голос по метке проходит
	ctx := withOwnVoice(context.Background())
	if text, ok, err := llm.Hear(ctx, p, audio, "audio/ogg", "ru"); !ok || err != nil || text != "услышал" {
		t.Fatalf("свой голос: %q ok=%v %v", text, ok, err)
	}
	// модель без слуха (обычная текстовая) отвечает «не умею», а не падает
	fake := &fakeProvider{answer: "текст"}
	e.providers["текстовая"] = fake
	if _, ok, err := llm.Hear(ctx, e.provider("текстовая"), audio, "audio/ogg", "ru"); ok || err != nil {
		t.Fatalf("текстовая модель: ok=%v err=%v", ok, err)
	}
}

// Роль не назначена — голосовое честно «не распознано: не выбрана модель», а
// в сеть не идёт ничего.
func TestNoModelForSpeech(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	_, _, err := e.Hear(context.Background(), VoiceIn{Audio: speechOgg(3), MIME: "audio/ogg", Own: true})
	if err == nil || err.Error() != "не выбрана модель" {
		t.Fatalf("без модели: %v", err)
	}
	if _, _, err := e.Hear(context.Background(), VoiceIn{MIME: "audio/ogg", Own: true}); err == nil {
		t.Fatal("пустая запись принята")
	}
}

// Запись больше предела не уходит провайдеру (25 МБ — столько принимает
// OpenAI): сказать об этом заранее дешевле, чем получить 413.
func TestTooBigRecordIsRefusedBeforeNetwork(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	f := newEars(t, "")
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "")
	setEarsRole(t, e, "слух")
	if err := e.store.SetSetting("speech.max_mb", "1"); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.Hear(context.Background(), VoiceIn{Audio: make([]byte, 2<<20), MIME: "audio/mpeg", Own: true})
	if err == nil || !strings.Contains(err.Error(), "больше 1 МБ") || f.count() != 0 {
		t.Fatalf("%v, запросов %d", err, f.count())
	}
}

// Три отказа подряд — роль отдыхает, и человек получает уведомление, как у
// остальных ролей; пока она отдыхает, обращений к модели нет.
func TestSpeechRoleRestsAfterThreeFailures(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	f := newEars(t, "")
	f.status = http.StatusInternalServerError
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "")
	setEarsRole(t, e, "слух")
	for i := 0; i < 3; i++ {
		if _, _, err := e.Hear(context.Background(), VoiceIn{Audio: speechOgg(2), MIME: "audio/ogg", Own: true}); err == nil {
			t.Fatal("отказ провайдера принят за успех")
		}
	}
	hits := f.count()
	_, _, err := e.Hear(context.Background(), VoiceIn{Audio: speechOgg(2), MIME: "audio/ogg", Own: true})
	if err == nil || !strings.Contains(err.Error(), "передышка") || f.count() != hits {
		t.Fatalf("на передышке: %v, запросов %d → %d", err, hits, f.count())
	}
	if n, ok := e.store.OpenNotificationByKey("role:" + RoleTranscribe); !ok || !strings.Contains(n.Title, "не отвечает") {
		t.Fatal("уведомление о молчащей роли не появилось")
	}
	// починили — первый же успех снимает передышку
	f.mu.Lock()
	f.status, f.text = 0, "ожил"
	f.mu.Unlock()
	e.clearRoleFails(RoleTranscribe)
	if text, _, err := e.Hear(context.Background(), VoiceIn{Audio: speechOgg(2), MIME: "audio/ogg", Own: true}); err != nil || text != "ожил" {
		t.Fatalf("после починки: %q, %v", text, err)
	}
}

// Бюджет действует и на распознавание: исчерпанный лимит — отказ до сети.
func TestBudgetStopsHearing(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	e.installUsage()
	f := newEars(t, "текст")
	connectEars(t, e, f, "слух", "gpt-4o-mini-transcribe", false, "$0.003 / мин")
	setEarsRole(t, e, "слух")
	if err := e.store.AddBudget(&store.Budget{Consumer: "core:telegram", Kind: "money", Period: "day", Limit: 0.0001, Action: "warn"}); err != nil {
		t.Fatal(err)
	}
	in := VoiceIn{Audio: speechOgg(60), MIME: "audio/ogg", Own: true, Consumer: "core:telegram"}
	if _, _, err := e.Hear(context.Background(), in); err != nil {
		t.Fatalf("первое распознавание: %v", err)
	}
	if _, _, err := e.Hear(context.Background(), in); err == nil || !strings.Contains(err.Error(), "бюджет") {
		t.Fatalf("после превышения: %v", err)
	}
}

func TestReasoningEffortOfChat(t *testing.T) {
	for level, want := range map[string]string{EffortFast: "low", EffortNormal: "", EffortDeep: "high", "": ""} {
		if got := reasoningEffort(level); got != want {
			t.Errorf("%q → %q, ждали %q", level, got, want)
		}
	}
}

// effortModel — модель, которая помнит, как долго её просили думать.
type effortModel struct {
	mu      sync.Mutex
	efforts []string
}

func (m *effortModel) Name() string { return "тестовая" }

func (m *effortModel) Complete(ctx context.Context, prompt string) (string, error) {
	m.mu.Lock()
	m.efforts = append(m.efforts, llm.EffortOf(ctx))
	m.mu.Unlock()
	return "ответ", nil
}

func (m *effortModel) CompleteStream(ctx context.Context, prompt string, on func(string)) (string, error) {
	return m.Complete(ctx, prompt)
}

// «Думает быстро / подробно» у чата доезжает до провайдера как reasoning_effort
// (провайдер отправит его только моделям OpenAI с рассуждением), а «обычно»
// не передаёт ничего: модель решает сама.
func TestChatEffortReachesProvider(t *testing.T) {
	m := &effortModel{}
	e, st := newTestEngine(t, "модель", m)
	for level, want := range map[string]string{EffortFast: "low", EffortDeep: "high", EffortNormal: ""} {
		chat, err := st.CreateChat("проба " + level)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetChatEffort(chat, level); err != nil {
			t.Fatal(err)
		}
		if err := e.ChatReply(context.Background(), chat, "вопрос"); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		got := m.efforts[len(m.efforts)-1]
		m.mu.Unlock()
		if got != want {
			t.Errorf("%q: до провайдера дошло %q, ждали %q", level, got, want)
		}
	}
}

package engine

// Голос: Сёма слышит. Служебная роль «Распознавание речи» и настройки вокруг
// неё. Провайдер OpenAI-совместимый: /audio/transcriptions переводит запись в
// текст; какая модель это умеет, решает её название (gpt-4o-mini-transcribe,
// gpt-4o-transcribe, whisper-1) или отметка человека, а не тип подключения.
//
// Главное место осторожности — класс. Аудио до распознавания не
// классифицируется: что в нём сказано, неизвестно, и `outsideCut` с его
// приметами здесь бессилен. Поэтому у голоса своя мерка — не «что сказано»,
// а «чей голос»: настройка `speech.outside`.
//
//   - own (по умолчанию) — облачной модели уходят только голосовые самого
//     владельца: админ-чат Telegram, загруженные им файлы и его просьбы в чате;
//   - all — любые;
//   - never — никакие.
//
// Доверенной модели (своему Whisper) можно всё, как и с текстом. Голосовое
// из чужого чата при `own` облачной модели не уходит вовсе, и в переписке
// остаётся честная строка «голосовое, не распознано: …». Распознанный
// текст дальше живёт по обычным правилам классов.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Настройки речи. Всё, что здесь можно назвать числом или словом, лежит
// в базе и правится на «Модели и роли → Роли»: чем слушать — строка роли в
// таблице, остальное — «Тонкая настройка» той же вкладки.
const (
	speechOutsideKey = "speech.outside"  // own | all | never
	speechLangKey    = "speech.language" // язык распознавания: ru
	speechFilesKey   = "speech.files"    // upload | off — откуда записи распознаются сами
	speechMaxMBKey   = "speech.max_mb"   // предел размера записи
)

// Значения настройки speech.outside.
const (
	speechOwn   = "own"
	speechAll   = "all"
	speechNever = "never"
)

// Значения настройки speech.files.
const (
	speechFilesUpload = "upload"
	speechFilesOff    = "off"
)

const (
	defaultSpeechLang  = "ru"
	defaultSpeechMaxMB = 25 // столько принимает /audio/transcriptions

	// hearingHint — модель, которую Сёма советует поставить на роль: самая
	// дешёвая из умеющих, $0.003 за минуту звука по прайсу OpenAI. Подсказка,
	// а не цена: деньги считаются по цене, которую человек задал у модели.
	hearingHint = "gpt-4o-mini-transcribe"
)

// errNoSpeechModel — роль речи ещё никому не отдана.
var errNoSpeechModel = errors.New("не выбрана модель")

// SpeechOutside — чьи голосовые уходят облачной модели (`speech.outside`).
func (e *Engine) SpeechOutside() string {
	switch v := strings.ToLower(strings.TrimSpace(e.store.SettingText(speechOutsideDef))); v {
	case speechAll, speechNever:
		return v
	}
	return speechOwn
}

func (e *Engine) SetSpeechOutside(mode string) error {
	switch mode {
	case speechOwn, speechAll, speechNever:
		return e.store.SetSetting(speechOutsideKey, mode)
	}
	return fmt.Errorf("режим бывает own, all или never")
}

// SpeechLanguage — язык распознавания для формы на экране; пусто — модель
// определяет язык сама.
func (e *Engine) SpeechLanguage() string { return e.speechLang() }

// SetSpeechLanguage — код языка («ru»). Пустая строка — осознанный выбор
// «пусть модель определит сама», а не «сбросить на умолчание».
func (e *Engine) SetSpeechLanguage(lang string) error {
	return e.store.SetSetting(speechLangKey, strings.ToLower(strings.TrimSpace(lang)))
}

func (e *Engine) speechLang() string {
	// пустой язык — осознанный выбор, и от «не задано» его отличает только
	// отсутствие строки: Setting отдаёт умолчание, лишь когда строки нет
	return strings.TrimSpace(e.store.SettingText(speechLangDef))
}

// speechMaxBytes — предел размера записи (`speech.max_mb`).
func (e *Engine) speechMaxBytes() int64 {
	return int64(e.intSetting(speechMaxMBKey, 0, defaultSpeechMaxMB)) << 20
}

// SpeechFiles — распознаются ли загруженные человеком аудиофайлы (`speech.files`):
// upload или off.
func (e *Engine) SpeechFiles() string {
	if strings.TrimSpace(e.store.SettingText(speechFilesDef)) == speechFilesOff {
		return speechFilesOff
	}
	return speechFilesUpload
}

func (e *Engine) SetSpeechFiles(mode string) error {
	if mode != speechFilesUpload && mode != speechFilesOff {
		return fmt.Errorf("режим бывает upload или off")
	}
	return e.store.SetSetting(speechFilesKey, mode)
}

// speechFromSource — распознаётся ли запись этого источника сама, без
// просьбы. Только загрузка руками: аудио из почты и папок — это деньги и
// чужие голоса, массово их не расшифровывают (`speech.files` = off гасит и
// загрузку).
func (e *Engine) speechFromSource(kind string) bool {
	return e.SpeechFiles() == "upload" && kind == store.SourceUpload
}

// ---- умения модели ----

var reHearsByName = regexp.MustCompile(`(?i)transcribe|whisper|(^|[-_/.])stt($|[-_.])`)

// hearsByName — умеет ли модель слушать по её названию у провайдера: OpenAI,
// Groq и локальные серверы называют речевые модели так, что ошибиться трудно.
// Отметка человека это дополняет, а не заменяет.
func hearsByName(model string) bool { return reHearsByName.MatchString(model) }

// CanHear — модель умеет распознавать речь. Реализован метод только у
// OpenAI-совместимого подключения: у остальных видов провайдера его нет.
func (c ModelConf) CanHear() bool {
	return c.Type == "openai" && (c.Hears || hearsByName(c.Model))
}

// modelConfOf — описание модели по имени: из базы, а модель из config.yaml
// (описана файлом) приводится к тому же виду.
func (e *Engine) modelConfOf(name string) (ModelConf, bool) {
	if c, ok := e.modelConf(name); ok {
		return c, true
	}
	if pc, ok := e.conf.LLM.Providers[name]; ok {
		return ModelConf{Name: name, Type: pc.Type, BaseURL: pc.BaseURL, Model: pc.Model}, true
	}
	return ModelConf{}, false
}

// ModelHears — модель слышит: распознаёт речь.
func (e *Engine) ModelHears(name string) bool {
	c, ok := e.modelConfOf(name)
	return ok && c.CanHear()
}

// ModelHearsBy — слышит ли модель и по какой причине: byName — умение видно
// по названию, отмечать его руками не нужно и снимать нельзя.
func (e *Engine) ModelHearsBy(name string) (hears, byName bool) {
	c, ok := e.modelConfOf(name)
	if !ok || !c.CanHear() {
		return false, false
	}
	return true, hearsByName(c.Model)
}

// HearingModels — имена моделей с умением «слышит»: из них выбирается роль
// «Распознавание речи». Остальные на неё не назначаются.
func (e *Engine) HearingModels() []string {
	var out []string
	for _, n := range e.ProviderNames() {
		if e.ModelHears(n) {
			out = append(out, n)
		}
	}
	return out
}

// defaultHearingModel — чем распознавать, пока человек не выбрал. Сначала
// своя модель: она бесплатна и голос не покидает машину; потом самая
// дешёвая из облачных — gpt-4o-mini-transcribe. Остальные умеющие (у них
// цена вдвое выше) сами не выбираются: гадать между ними за деньги
// владельца нельзя. Зовётся при каждом чтении списка ролей, поэтому список
// моделей берётся один раз.
func (e *Engine) defaultHearingModel() string {
	cheap := ""
	for _, c := range e.userModels() {
		if !c.CanHear() || !e.hasProvider(c.Name) {
			continue
		}
		if c.Type != "cli" && c.Trusted {
			return c.Name
		}
		if cheap == "" && strings.Contains(strings.ToLower(c.Model), hearingHint) {
			cheap = c.Name
		}
	}
	if cheap != "" {
		return cheap
	}
	return roleNoneName
}

// SetModelHears — отметка человека для модели с названием, по которому
// умение не угадать (свой сервер с моделью «мой-вошпер»).
func (e *Engine) SetModelHears(name string, on bool) error {
	list, err := e.userModelsE()
	if err != nil {
		return err
	}
	found := false
	for i := range list {
		if list[i].Name == name {
			list[i].Hears, found = on, true
		}
	}
	if !found {
		return fmt.Errorf("модель %q правится не из интерфейса", name)
	}
	return e.saveUserModels(list)
}

// checkRoleAbility — на роль речи ставится только модель с нужным умением.
func (e *Engine) checkRoleAbility(role, model string) error {
	if role != RoleTranscribe || model == "" || model == roleNoneName {
		return nil
	}
	if !e.ModelHears(model) {
		return fmt.Errorf("модель «%s» не слышит речь: на роль «%s» ставятся модели с умением «слышит» (например, %s); "+
			"для модели с другим названием умение отмечается в её карточке", model, role, hearingHint)
	}
	return nil
}

// ---- чей голос ----

type ownVoiceKey struct{}

// withOwnVoice помечает вызов: это голос самого владельца (админ-чат
// Telegram, его загрузка, его прямая просьба). Дверь по этой метке пускает
// запись к облачной модели при `speech.outside` = own.
func withOwnVoice(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownVoiceKey{}, true)
}

func ownVoiceOf(ctx context.Context) bool {
	v, _ := ctx.Value(ownVoiceKey{}).(bool)
	return v
}

// speechAllowed — можно ли показать запись этой модели. Доверенной — всегда;
// облачной — по `speech.outside`. Одна функция и у двери, и у вызывающего:
// решение и его проверка разойтись не могут.
func (e *Engine) speechAllowed(ctx context.Context, model string) (bool, string) {
	if !e.ModelIsCloud(model) {
		return true, ""
	}
	switch e.SpeechOutside() {
	case speechAll:
		return true, ""
	case speechNever:
		return false, "голосовые не уходят облачной модели (настройка «Чьи голосовые уходят наружу» — никакие)"
	}
	if ownVoiceOf(ctx) {
		return true, ""
	}
	return false, "голосовые из чужих чатов не уходят облачной модели: назначьте на роль доверенную модель " +
		"или разрешите все голосовые в «Настройки → Модели и роли → Роли → Тонкая настройка → Распознавание речи»"
}

var errGateAudio = errors.New("не пущено дверью: голосовое недоверенной модели")

// ---- распознавание ----

// VoiceIn — запись, которую надо распознать.
type VoiceIn struct {
	Audio []byte
	MIME  string
	// Own — голос самого владельца: только такой уходит облачной модели при
	// `speech.outside` = own
	Own      bool
	Consumer string // кто платит: core:telegram, core:files…
}

// Hear распознаёт речь моделью роли «Распознавание речи» и называет модель,
// которая справилась. Ошибка всегда объяснима человеку: она встаёт в
// переписку строкой «не распознано: …».
//
// Роль работает как остальные: основная модель, запасная, таймаут, три отказа
// подряд — передышка и уведомление. Но отказ по классу — решение, а не сбой:
// обращения к модели тогда не происходит, в журнале расхода нет красной
// строки, и три таких отказа роль на передышку не отправят.
func (e *Engine) Hear(ctx context.Context, in VoiceIn) (text, model string, err error) {
	if len(in.Audio) == 0 {
		return "", "", errors.New("пустая запись")
	}
	if limit := e.speechMaxBytes(); int64(len(in.Audio)) > limit {
		return "", "", fmt.Errorf("запись больше %d МБ — столько принимает распознавание", limit>>20)
	}
	ctx = llm.WithConsumer(ctx, in.Consumer)
	if err := e.budgetCheck(ctx); err != nil {
		return "", "", err
	}
	// класса у записи нет, пока её не расшифровали: пишем в журнал как
	// чувствительное — молчание классификатора не разрешение
	ctx = llm.WithClass(ctx, store.ClassSensitive)
	if in.Own {
		ctx = withOwnVoice(ctx)
	}

	r := e.Role(RoleTranscribe)
	var models []string
	for _, m := range []string{r.Model, r.Spare} {
		if m != "" && m != roleNoneName && (len(models) == 0 || m != models[0]) {
			models = append(models, m)
		}
	}
	if len(models) == 0 {
		return "", "", errNoSpeechModel
	}
	timeout := time.Duration(r.Timeout) * time.Second
	if timeout <= 0 {
		timeout = time.Minute
	}
	until, resting := e.roleCooldown(RoleTranscribe)
	lang := e.speechLang()
	var first error
	remember := func(err error) {
		if first == nil {
			first = err
		}
	}
	for _, m := range models {
		main := m == r.Model
		if main && resting {
			remember(fmt.Errorf("модель %q не отвечала подряд — передышка до %s", m, until.Format("15:04")))
			continue
		}
		if ok, why := e.speechAllowed(ctx, m); !ok {
			remember(errors.New(why))
			continue
		}
		p := e.provider(m)
		if p == nil {
			remember(fmt.Errorf("модель %q не подключена", m))
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		out, can, err := llm.Hear(cctx, p, in.Audio, in.MIME, lang)
		cancel()
		if err == nil && !can {
			remember(fmt.Errorf("модель %q не умеет распознавать речь", m))
			continue
		}
		if err == nil {
			if main {
				e.noteRole(RoleTranscribe, m)
				e.clearRoleFails(RoleTranscribe)
			} else {
				e.noteRole(RoleTranscribe, m+" (запасная)")
			}
			if out = strings.TrimSpace(out); out == "" {
				return "", m, errors.New("речи в записи не нашлось")
			}
			return out, m, nil
		}
		log.Printf("роль %q: модель %s не справилась: %v", RoleTranscribe, m, err)
		if main {
			e.noteRoleFail(RoleTranscribe, m, err)
		}
		if ctx.Err() != nil {
			return "", m, fmt.Errorf("работа прервана по времени: %w", ctx.Err())
		}
		remember(humanModelErr(err))
	}
	return "", "", first
}

// ---- цена звука ----

var rePerMinute = regexp.MustCompile(`(?i)/\s*(мин|min)`)

// parsePerMinute — цена за минуту звука: «$0.003 / мин». У моделей речи
// прайс поминутный, а не за миллион токенов; токены gpt-4o-transcribe
// называет, но платят за секунды.
func parsePerMinute(price string) (v float64, cur string, ok bool) {
	if !rePerMinute.MatchString(price) {
		return 0, "", false
	}
	v, cur = parsePrice(price)
	return v, cur, v > 0
}

// costOfAudio — деньги за звук: минуты на цену за минуту; нет её, но есть
// цена за токены — по токенам, как у обычной модели. priced=false — цены
// нет вовсе, и в журнале об этом сказано словами, а не молчаливым нулём.
func (e *Engine) costOfAudio(model string, u llm.Usage) (cost float64, cur string, priced bool) {
	if !e.ModelIsRemote(model) {
		return 0, "", true // своя машина: платить некому
	}
	for _, c := range e.userModels() {
		if c.Name != model {
			continue
		}
		if per, cur, ok := parsePerMinute(c.Price); ok {
			return u.AudioSec / 60 * per, cur, true
		}
		if cost, cur := e.costOfUsage(model, u); cost > 0 {
			return cost, cur, true
		}
	}
	return 0, "", false
}

// audioNote — приписка к записи расхода: сколько звука и, если цена не
// названа, что деньги не посчитаны.
func audioNote(sec float64, priced bool) string {
	note := llm.AudioLabel(sec)
	if !priced {
		note += " · цена за минуту не задана — деньги не посчитаны"
	}
	return note
}

package llm

// Учёт расхода: каждый вызов модели сообщает наружу, кто спрашивал (из
// контекста), сколько токенов ушло и пришло (из ответа провайдера или по
// оценке), сколько длился и чем кончился. Пишет один колбэк — ядро.

import (
	"context"
	"time"
	"unicode"
)

// Usage — токены одного вызова. Exact=false — оценка по длине текста.
type Usage struct {
	In    int
	Out   int
	Exact bool
	// Cached — часть входа, попавшая в кэш провайдера. У DeepSeek вход из
	// кэша дешевле обычного примерно в тридцать раз, и без этого числа
	// наш счёт завышен настолько же. Reasoning — токены размышления,
	// входящие в выход: их считать отдельно не надо, но видеть полезно.
	Cached    int
	Reasoning int
	// Cost — стоимость, посчитанная самим провайдером, и валюта. Perplexity
	// возвращает её в ответе с разбивкой на вход, выход и плату за поиск, и
	// это точнее любой нашей прикидки: у нас вход и выход шли по одной цене,
	// а плата за поиск не считалась вовсе. Ноль — провайдер цену не назвал,
	// считаем по прайсу модели.
	Cost     float64
	Currency string
	// AudioSec — сколько секунд звука распознано за вызов. Цена такого вызова —
	// за минуту звука, а не за токены (gpt-4o-transcribe токены называет, но
	// прайс у него поминутный).
	AudioSec float64
}

// Call — что случилось: модель, потребитель, токены, секунды, ошибка.
type Call struct {
	Model    string
	Consumer string
	Usage    Usage
	Seconds  float64
	Err      error
	Kind     string // text | stream | image | transcribe
	// Prompt и Answer — что ушло модели и что она ответила: журнал
	// отправленного пишет их после масок секретов.
	Prompt string
	Answer string
	// Retryable — неудачу переделают (работа вернулась в очередь): такое
	// считается повтором, а не отказом.
	Retryable bool
	// ClassKind и ClassID — сущность, которая ушла модели (письмо, файл).
	// Журнал берёт класс от неё, а не гадает по тексту промпта: у картинки
	// в промпте одна служебная инструкция, и скан паспорта записывался как
	// «обычное». Пусто — класс считается по тексту, как прежде.
	ClassKind string
	ClassID   int64
	// Class — класс, посчитанный вызывающим, когда записи в базе ещё нет.
	Class string
}

// OnCall — колбэк учёта; ядро ставит его при запуске. nil — не учитываем.
var OnCall func(Call)

type retryKey struct{}

// WithRetryable отмечает контекст: неудача внутри него — повтор, работу
// переделает очередь. Ставит очередь, чат не ставит никогда.
type searchKey struct{}

// WithWebSearch помечает вызов как поисковый: провайдер, умеющий искать
// сам (Anthropic), включит свой инструмент поиска. Остальные не заметят.
func WithWebSearch(ctx context.Context) context.Context {
	return context.WithValue(ctx, searchKey{}, true)
}

// WebSearchOf — просили ли поиск в этом вызове.
func WebSearchOf(ctx context.Context) bool {
	v, _ := ctx.Value(searchKey{}).(bool)
	return v
}

func WithRetryable(ctx context.Context) context.Context {
	return context.WithValue(ctx, retryKey{}, true)
}

func RetryableOf(ctx context.Context) bool {
	v, _ := ctx.Value(retryKey{}).(bool)
	return v
}

type effortKey struct{}

// WithEffort — сколько думать моделям с рассуждением: low, medium или high
// (reasoning_effort у OpenAI). Пусто — как решит сама модель. Остальные
// провайдеры метку не замечают: у них уровень размышления просят словами в
// промпте.
func WithEffort(ctx context.Context, level string) context.Context {
	if level == "" {
		return ctx
	}
	return context.WithValue(ctx, effortKey{}, level)
}

// EffortOf — уровень из контекста; пусто — не просили.
func EffortOf(ctx context.Context) string {
	v, _ := ctx.Value(effortKey{}).(string)
	return v
}

type consumerKey struct{}

// WithConsumer подписывает контекст: кто платит за вызовы внутри него.
func WithConsumer(ctx context.Context, consumer string) context.Context {
	if consumer == "" {
		return ctx
	}
	return context.WithValue(ctx, consumerKey{}, consumer)
}

// WithClass помечает контекст классом того, что уходит модели. Нужен там,
// где сущности в базе ещё нет: картинка разбирается до вставки записи, а
// класс к этому моменту уже посчитан по имени файла и тексту нашего OCR.
func WithClass(ctx context.Context, class string) context.Context {
	if class == "" {
		return ctx
	}
	return context.WithValue(ctx, classKey{}, class)
}

type classKey struct{}

// ClassOf — класс из контекста; пусто — не помечали.
func ClassOf(ctx context.Context) string {
	if c, ok := ctx.Value(classKey{}).(string); ok {
		return c
	}
	return ""
}

// WithSubject помечает контекст сущностью, о которой идёт вызов: письмо или
// файл. Провайдеры кладут её в Call, а журнал берёт оттуда класс — гадать по
// тексту промпта было неверно для картинок.
func WithSubject(ctx context.Context, kind string, id int64) context.Context {
	if kind == "" || id == 0 {
		return ctx
	}
	return context.WithValue(ctx, subjectKey{}, subject{kind, id})
}

type subjectKey struct{}

type subject struct {
	kind string
	id   int64
}

// SubjectOf — сущность из контекста; пусто — вызов не про одну вещь.
func SubjectOf(ctx context.Context) (string, int64) {
	if s, ok := ctx.Value(subjectKey{}).(subject); ok {
		return s.kind, s.id
	}
	return "", 0
}

// ConsumerOf — подпись из контекста; пусто — неизвестно (ядро).
func ConsumerOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if c, ok := ctx.Value(consumerKey{}).(string); ok {
		return c
	}
	return ""
}

// Estimate — грубая оценка токенов по тексту: кириллица дороже латиницы.
func Estimate(s string) int {
	if s == "" {
		return 0
	}
	cyr, other := 0, 0
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			cyr++
		} else {
			other++
		}
	}
	return cyr/3 + other/4 + 1
}

// report сообщает о вызове; когда провайдер токенов не дал — оцениваем.
// Report — учёт вызова: провайдер зовёт его после каждого обращения к
// модели. Экспортирован, чтобы свои провайдеры и поддельные модели в тестах
// попадали в тот же журнал, что и встроенные.
func Report(ctx context.Context, model, kind, prompt, answer string, u Usage, started time.Time, err error) {
	report(ctx, model, kind, prompt, answer, u, started, err)
}

func report(ctx context.Context, model, kind, prompt, answer string, u Usage, started time.Time, err error) {
	if OnCall == nil {
		return
	}
	if !u.Exact {
		est := Usage{In: Estimate(prompt), Out: Estimate(answer), Exact: false}
		if u.AudioSec > 0 {
			// у звука в prompt или answer стоит подпись «аудио 9 с», а не
			// текст: токены по ней не считаются, берём то, что назвал
			// сам провайдер вызова (оценку слов он делает по настоящему тексту)
			est = Usage{In: u.In, Out: u.Out, Exact: false, AudioSec: u.AudioSec}
		}
		u = est
	}
	kindOf, idOf := SubjectOf(ctx)
	OnCall(Call{Class: ClassOf(ctx), Model: model, Consumer: ConsumerOf(ctx), Usage: u, Seconds: time.Since(started).Seconds(), Err: err, Kind: kind,
		Prompt: prompt, Answer: answer, Retryable: RetryableOf(ctx),
		ClassKind: kindOf, ClassID: idOf})
}

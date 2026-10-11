package engine

// Дверь перед моделью: одна проверка на весь собранный промпт.
//
// Раньше проверка стояла в пяти местах сборки контекста и смотрела на
// отдельные сущности — письмо, файл, картинку. Текст самого разговора не
// смотрел никто, и 8 сентября номер налогоплательщика уехал облачной
// модели в реплике, хотя ни одна сущность секретной не считалась.
//
// Вторая беда была в том, что решение и запись журнала считали разные
// функции с разным входом: страж смотрел на сущности, журнал — на промпт.
// Разойтись им было обязано. Теперь класс возвращает та же функция,
// которая принимает решение, и в журнал идёт именно он.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Решение двери: пускать ли промпт к этой модели и почему.
type Decision struct {
	Allow  bool
	Class  string
	Reason string // чем вызван класс: слово, номер, машиночитаемая строка
	Denial string // человеку и в журнал: почему не пустили
}

// BeforeModel — проверить собранный промпт перед обращением к модели.
// Доверенной модели можно всё: наружу ничего не уходит. Недоверенной
// секретное не отдаётся никогда, чувствительное — по режиму.
//
// Сущность вызова берётся из контекста (`llm.WithSubject`): если человек
// пометил это письмо или файл руками, его пометка сильнее любой приметы —
// и вверх, и вниз. Так делает Outlook: наследованная метка повышает, но
// ручную не перетирает. Иначе человек, снявший «секретное» с письма,
// которое просто цитирует слово «паспорт», не смог бы его обсудить вовсе.
func (e *Engine) BeforeModel(ctx context.Context, model, prompt string) Decision {
	class, reason := e.ContentClass(prompt)
	if kind, id := llm.SubjectOf(ctx); kind != "" && id > 0 {
		if m, ok := e.store.ClassMarkOf(kind, id); ok && m.By != "" && m.Class != "" {
			class, reason = m.Class, "пометка человека ("+m.By+")"
		}
	}
	r := Decision{Allow: true, Class: class, Reason: reason}
	if !e.ModelIsCloud(model) {
		return r // доверенная: решает не класс, а доверие человека
	}
	switch class {
	case store.ClassSecret:
		r.Allow, r.Denial = false, "секретное не отдаётся недоверенной модели"
		return r
	case store.ClassSensitive:
		if e.SensitiveMode() == "allow" {
			return r
		}
		r.Allow, r.Denial = false, "чувствительное без согласия человека"
		return r
	}
	return r
}

// GateDenial — текст ошибки, который увидит человек. Отдельной строкой,
// чтобы он был одинаков везде и его можно было узнать в журнале.
func GateDenial(r Decision) string {
	if r.Allow {
		return ""
	}
	has := "Сёма не отдал это недоверенной модели: " + r.Denial
	if r.Reason != "" {
		has += " (" + r.Reason + ")"
	}
	return has + ". Назначьте доверенную модель на эту роль или пометьте содержимое вручную."
}

// PromptClass — класс для журнала отправок. Тот же расчёт, что у двери:
// если журнал считает класс своей меркой, он рано или поздно разойдётся с
// тем, по чему принято решение, — так и случилось.
func (e *Engine) PromptClass(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return store.ClassPlain
	}
	class, _ := e.ContentClass(prompt)
	return class
}

// ContentClass — класс текста и примета, по которой он такой. К
// приметам из `КлассТекста` добавлен список своих значений: он ловит
// случай, который с ними не ловится вовсе, — свой номер в реплике без
// слова-подсказки рядом.
func (e *Engine) ContentClass(text string) (class, reason string) {
	if kind, has := e.hasOwnValue(text); has {
		return store.ClassSecret, "свой " + kind + " из списка своих значений"
	}
	return TextClass(text)
}

// gate — обёртка провайдера: проверяет собранный промпт перед отправкой.
// Реализует и Streamer, если умеет обёрнутый, — иначе чат потерял бы
// потоковый ответ и стал бы отвечать молчанием на минуту.
type gate struct {
	llm.Provider
	e *Engine
	// имя — то, под которым модель заведена у человека. Именно оно
	// решает доверие: провайдер зовётся своим именем («openai»), а
	// доверие объявлено человеком имени в справочнике
	name string
}

func (d *gate) Complete(ctx context.Context, prompt string) (string, error) {
	if err := d.check(ctx, prompt); err != nil {
		return "", err
	}
	return d.Provider.Complete(ctx, prompt)
}

func (d *gate) CompleteStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	if err := d.check(ctx, prompt); err != nil {
		return "", err
	}
	s, ok := d.Provider.(llm.Streamer)
	if !ok {
		return d.Provider.Complete(ctx, prompt)
	}
	return s.CompleteStream(ctx, prompt, onDelta)
}

// CompleteImage — картинка через дверь. Метод объявлен всегда, иначе
// `llm.SeeImage` не увидит Vision у обёртки и решит, что модель картинок
// не принимает вовсе: обёртка вокруг интерфейса продвигает только его
// методы. Обёрнутый провайдер картинок не умеет — отвечаем оговорённой
// ошибкой, и SeeImage понимает её как «не умеет», а не как сбой.
func (d *gate) CompleteImage(ctx context.Context, prompt string, image []byte, mime string) (string, error) {
	v, ok := d.Provider.(llm.Vision)
	if !ok {
		return "", llm.ErrNoImages
	}
	if err := d.check(ctx, prompt); err != nil {
		return "", err
	}
	return v.CompleteImage(ctx, prompt, image, mime)
}

// Transcribe — запись через дверь. Как и CompleteImage, метод объявлен
// всегда: `llm.Hear` иначе не увидел бы Transcriber у обёртки и решил бы,
// что модель речь не понимает. Обёрнутый провайдер речи не знает — отвечаем
// оговорённой ошибкой.
//
// Аудио не классифицируется до распознавания: что в нём сказано, неизвестно.
// Поэтому вместо класса текста дверь смотрит на голос — чей он и что решил
// человек про `speech.outside` (см. speechAllowed).
func (d *gate) Transcribe(ctx context.Context, audio []byte, mime, lang string) (string, error) {
	t, ok := d.Provider.(llm.Transcriber)
	if !ok {
		return "", llm.ErrNoTranscribe
	}
	if allow, why := d.e.speechAllowed(ctx, d.name); !allow {
		llm.Report(ctx, d.name, "gate", "аудио", "", llm.Usage{}, time.Now(), errGateAudio)
		log.Printf("дверь: %s не получил голосовое — %s", d.name, why)
		return "", fmt.Errorf("Сёма не отдал голосовое недоверенной модели: %s", why)
	}
	return t.Transcribe(ctx, audio, mime, lang)
}

func (d *gate) check(ctx context.Context, prompt string) error {
	r := d.e.BeforeModel(ctx, d.name, prompt)
	if r.Allow {
		return nil
	}
	// отказ виден в журнале расхода тем же путём, что и обычный вызов:
	// человек должен понимать, что обращение было и его не пустили
	llm.Report(ctx, d.name, "gate", clip(prompt, 400), "", llm.Usage{}, time.Now(), errGateDenial)
	log.Printf("дверь: %s не получил промпт — %s (%s)", d.name, r.Denial, r.Reason)
	return fmt.Errorf("%s", GateDenial(r))
}

var errGateDenial = fmt.Errorf("не пущено дверью: секретное недоверенной модели")

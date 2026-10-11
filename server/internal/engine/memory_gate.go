package engine

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// askMemory — единственная дверь роли «Память» к модели. Память сводит
// сессии и записи, а в них бывает секретное: тогда внешней модели текст не
// отдаётся — идёт доверенной, а нет её, честный отказ с советом. Без этой
// двери сведение сессии уносило класс «секретное» наружу (7 сентября 2026,
// core:memory → gpt-5.6-luna). allowSpare — можно ли звать запасную роли.
func (e *Engine) askMemory(ctx context.Context, prompt string) (string, string, error) {
	return e.askMemorySpare(ctx, prompt, true)
}

// withoutThinking — памяти не нужен ход мысли вслух, ей нужен короткий
// разбор. Локальная qwen3 на Pi выдаёт около семи знаков в секунду и
// тратила весь пятиминутный предел роли на «думание» перед ответом:
// 1950 знаков рассуждения — и обрыв по сроку, ни записи, ни кристалла.
// За двое суток так пропали 62 сведения из 181. Строка та же, что у
// быстрого усилия в чате: qwen понимает её как выключатель, остальные
// читают как просьбу отвечать коротко — вреда нет ни от кого.
const withoutThinking = "\nAnswer directly with the result only. Do not reason out loud. /no_think\n"

func (e *Engine) askMemorySpare(ctx context.Context, prompt string, allowSpare bool) (string, string, error) {
	return e.askRoleGuarded(ctx, RoleMemory, prompt+withoutThinking, allowSpare)
}

// askRoleGuarded — дверь для любой роли: текст класса «секретное» внешней
// модели роли не отдаётся — идёт доверенной, а без неё отказ с советом и
// запись в журнал отказов. Всё остальное — обычный AskRoleSpare. Каждый
// вызов роли в пакете идёт либо сюда, либо помечен причиной — за этим
// следит TestНетЧетвёртойДвери.
func (e *Engine) askRoleGuarded(ctx context.Context, role, prompt string, allowSpare bool) (string, string, error) {
	return e.askRoleClosed(ctx, role, prompt, allowSpare, false)
}

// askRoleClosed — то же, но с прямым указанием «этот текст наружу нельзя»:
// закрытая область памяти или закрытый ящик заперты не приметами в тексте,
// а решением человека, и дверь должна знать об этом до разбора.
func (e *Engine) askRoleClosed(ctx context.Context, role, prompt string, allowSpare, closed bool) (string, string, error) {
	r := e.Role(role)
	name := r.Model
	if name == roleNoneName || name == "" {
		return "", "", fmt.Errorf("роль «%s» без модели", role)
	}
	secret := closed || sentClass(prompt) == store.ClassSecret
	// запасная модель — та же дверь: доверенная основная с облачной
	// запасной иначе отдавала бы секретное наружу при первом же отказе
	if secret && allowSpare && r.Spare != "" && r.Spare != roleNoneName && e.ModelIsCloud(r.Spare) {
		allowSpare = false
	}
	if e.ModelIsCloud(name) && secret {
		// суд: подменить арбитра или судью общей доверенной моделью значит
		// дать одной модели судить саму себя — такое решает человек
		if role == RoleArbiter || role == RoleJudge {
			e.saveDenialRole(role, "секретное в команде: суд не собирается, решает человек", prompt)
			return "", "", fmt.Errorf("секретное в тексте: суд не собирается, решает человек")
		}
		if home := e.homeModel(); home != "" {
			if p := e.provider(home); p != nil {
				if err := e.budgetCheck(ctx); err != nil {
					return "", "", err
				}
				cctx, cancel := context.WithTimeout(llm.WithConsumer(ctx, "core:"+role), time.Duration(max(r.Timeout, 1))*time.Second)
				defer cancel()
				// наружу не уходит: доверенная модель выбрана по классу текста
				out, err := p.Complete(cctx, prompt)
				if err == nil {
					e.noteRole(role, home+" (доверенная по классу)")
				}
				return out, home, err
			}
		}
		e.saveDenialRole(role, "секретное наружу не отправляю — назначьте доверенную модель на «Моделях»", prompt)
		return "", "", errNoTrusted
	}
	// наружу не уходит: класс проверен строкой выше
	return e.AskRoleSpare(ctx, role, prompt, allowSpare)
}

// errNoTrusted — секретное есть, доверенной модели нет: работа ждёт.
var errNoTrusted = fmt.Errorf("секретное наружу не отправляю: назначьте доверенную модель на «Моделях»")

// saveDenialRole — отказ в «Что ушло» от имени роли, а не эмбеддера.
func (e *Engine) saveDenialRole(role, why, text string) {
	log.Printf("роль «%s»: %s", role, why)
	_ = e.store.AddSent(&store.SentRow{
		Consumer: "core:" + role, Model: e.Role(role).Model,
		Outside: true, Kind: "text", Class: sentClass(text),
		OK: false, Err: "не отправлено: " + why,
		Preview: clip(text, 200),
	})
}

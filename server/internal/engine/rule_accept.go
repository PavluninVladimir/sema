package engine

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Правило из переписки. Кнопка «Записать в инструкцию» ничего не меняла на
// вид: правило уходило в инструкцию молча, карточка оставалась
// «предложением», и человек нажимал ещё раз. 7 сентября 2026 в инструкции
// нашлось три копии одного правила. Теперь ответ виден в самой карточке, а
// повтор ничего не добавляет.

// AcceptRule — записать правило карточки в инструкцию модуля. msgID — номер
// сообщения с карточкой; текст берётся оттуда, а не из адреса кнопки:
// правило бывает длиной в тысячу знаков с переводами строк.
func (e *Engine) AcceptRule(module string, msgID int64) error {
	msg, err := e.store.ChatMsg(msgID)
	if err != nil || msg == nil {
		return fmt.Errorf("карточки правила больше нет")
	}
	var card ToolCard
	if err := json.Unmarshal([]byte(msg.Content), &card); err != nil || card.Rule == "" {
		return fmt.Errorf("это не карточка правила")
	}
	if card.RuleDone {
		return nil // уже записано: повтор ничего не меняет
	}
	if module == "" {
		module = card.Rule
	}
	if err := e.AppendModuleRule(module, card.Code); err != nil {
		return err
	}
	card.RuleDone = true
	card.Result = "записано в инструкцию модуля «" + module + "»"
	raw, err := json.Marshal(card)
	if err != nil {
		return err
	}
	return e.store.ReplaceChatMsg(msgID, string(raw))
}

// ruleAlreadyIn — правило уже есть в инструкции: сравниваем по строкам без
// лишних пробелов, чтобы не плодить почти одинаковые копии.
func ruleAlreadyIn(instructions, rule string) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	return strings.Contains(norm(instructions), norm(rule))
}

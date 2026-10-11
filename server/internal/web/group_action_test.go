package web

import (
	"strings"
	"testing"
)

// Кнопка группового действия помечается для клиента. Она имеет смысл
// только при отмеченных строках, и клиент переносит её в полосу выбора;
// без признака она навсегда осталась бы в заголовке раздела, где звала
// к себе всегда — даже когда нажать её нельзя.
//
// 10 сентября 2026 признаком считалось «data-target="$selected"» — а его
// у серверных действий нет вовсе: он ставится только клиентским («$…»).
// Перенос молча не работал, и заметить это можно было только в браузере.
func TestGroupActionMarked(t *testing.T) {
	group := string(attrs(Button{Name: "В спам выбранные", Action: "confirm_spam", Target: "$selected"}))
	if !strings.Contains(group, `data-bulk="1"`) {
		t.Errorf("нет признака группового действия: %s", group)
	}
	if !strings.Contains(group, `hx-include=".row-check input:checked"`) {
		t.Errorf("отметки не уходят на сервер: %s", group)
	}
	// обычная кнопка признака не получает: её переносить некуда
	plain := string(attrs(Button{Name: "Проверить", Action: "refresh"}))
	if strings.Contains(plain, "data-bulk") {
		t.Errorf("обычная кнопка помечена групповой: %s", plain)
	}
}

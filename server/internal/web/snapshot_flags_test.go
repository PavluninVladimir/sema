package web

// Признаки снимка в разметке (offline-snapshot, 4.0) — со стороны самой
// разметки: тест в `internal/api` проверяет живой путь `/ui` целиком, а
// здесь сторожатся ветки, до которых нынешние экраны не доходят.
//
// Ветка `attrs` — ровно такая: откладываемых действий сегодня два, и оба
// рисует шаблон композера, а не общая сборка кнопки. Значит поломку
// `attrs` проверка через `/ui` не заметит вовсе (проверено нарочной
// поломкой 27 сентября 2026: тест остался зелёным). Станет откладываемым
// что-то третье — оно приедет обычной кнопкой, и признак у неё обязан быть.

import (
	"strings"
	"testing"
)

func TestSnapshotFlagsOnScreenSection(t *testing.T) {
	build := func(scr Screen) string {
		t.Helper()
		out, err := Render(scr)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	// молчание — запрет: признаки пишутся всегда, «0» наравне с «1», иначе
	// отсутствие атрибута не отличить от разметки прежней сборки
	empty := build(Screen{Addr: "inbox"})
	for _, want := range []string{`data-storable="0"`, `data-own-device="0"`, `data-snapshot-differs="0"`} {
		if !strings.Contains(empty, want) {
			t.Errorf("в разметке нет %s", want)
		}
	}

	full := build(Screen{Addr: "inbox", Storable: true, OwnDevice: true,
		SnapshotDiffers: true, Dictionary: 22, SnapshotDays: 7})
	for _, want := range []string{`data-storable="1"`, `data-own-device="1"`,
		`data-snapshot-differs="1"`, `data-dictionary="22"`, `data-snapshot-days="7"`} {
		if !strings.Contains(full, want) {
			t.Errorf("в разметке нет %s", want)
		}
	}
}

// Кнопка, помеченная откладываемой, несёт признак; непомеченная — нет.
// Очередь исходящих узнаёт свои действия только по нему.
func TestDeferrableButtonCarriesFlag(t *testing.T) {
	out, err := Render(Screen{Addr: "chat:1", Buttons: []Button{
		{Action: "send_message", Target: "1", Name: "Отправить", Deferrable: true},
		{Action: "approve_ssh", Target: "7", Name: "Выполнить"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	markup := string(out)
	// кавычки в hx-vals экранированы как &#34;, поэтому ищем имя как есть
	i := strings.Index(markup, `send_message`)
	j := strings.Index(markup, `approve_ssh`)
	if i < 0 || j < 0 {
		t.Fatalf("кнопок нет в разметке:\n%s", markup)
	}
	// признак стоит у своей кнопки и только у неё: кнопок две, признак один
	if n := strings.Count(markup, `data-deferrable="1"`); n != 1 {
		t.Fatalf("откладываемых признаков %d, а кнопка такая одна:\n%s", n, markup)
	}
	k := strings.Index(markup, `data-deferrable="1"`)
	if !(k < i) || (j < i && k > j) {
		t.Errorf("признак откладываемости стоит не у той кнопки:\n%s", markup)
	}
}

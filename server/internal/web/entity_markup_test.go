package web

import (
	"strings"
	"testing"
)

// Окно «с … до …» рисуется двумя полями времени, а не текстом: формат
// «02:00» не нужно помнить. Приходят они двумя параметрами, name_from и
// name_to; часы без нуля добиваются, иначе браузерное поле их не принимает.
func TestTimespanFieldIsTwoTimeInputs(t *testing.T) {
	html := buildBlock(t, Block{Kind: "форма", ID: "host-auto", Style: "rows", Deferred: true,
		Fields: []Field{{Kind: "timespan", Name: "window", AnchorID: "host-auto-window", Label: "Окно", Value: "2:00-06:30",
			HasDefault: true, Default: "", NotDefault: true}}})
	for _, want := range []string{
		`type="time" name="window_from" value="02:00"`,
		`type="time" name="window_to" value="06:30"`,
		`>круглые сутки</button>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("в разметке окна нет %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, `name="window"`) {
		t.Error("окно ушло бы одним параметром: склеивать его на клиенте нечем")
	}
	// круглые сутки — оба поля пусты
	empty := buildBlock(t, Block{Kind: "форма", ID: "f", Style: "rows", Deferred: true,
		Fields: []Field{{Kind: "timespan", Name: "window", AnchorID: "f-window", Label: "Окно"}}})
	if !strings.Contains(empty, `name="window_from" value=""`) || !strings.Contains(empty, `name="window_to" value=""`) {
		t.Errorf("пустое окно:\n%s", empty)
	}
}

// Вариант, который сейчас выбрать нельзя, серый и отключён; выбранный вариант
// не отключается никогда, даже если он в списке отключённых.
func TestSegmentsDisabledOptions(t *testing.T) {
	html := buildBlock(t, Block{Kind: "форма", ID: "f", Style: "rows", Deferred: true,
		Fields: []Field{{Kind: "segments", Name: "level", AnchorID: "f-level", Label: "Уровень",
			Options: []string{"ручное", "под судом", "полная"}, Value: "ручное", Disabled: []string{"под судом", "полная"}}}})
	if strings.Count(html, ` disabled>`) != 2 || strings.Count(html, "segment disabled") != 2 {
		t.Errorf("отключены не два варианта:\n%s", html)
	}
	picked := buildBlock(t, Block{Kind: "форма", ID: "f", Style: "rows", Deferred: true,
		Fields: []Field{{Kind: "segments", Name: "level", AnchorID: "f-level", Label: "Уровень",
			Options: []string{"ручное", "полная"}, Value: "полная", Disabled: []string{"полная"}}}})
	if strings.Contains(picked, "disabled") {
		t.Errorf("выбранный вариант отключён:\n%s", picked)
	}
}

// Редкие поля формы уходят под «Ещё N полей» в той же форме: их видно по
// требованию, а отправляются они вместе с остальными.
func TestMoreFieldsFoldInsideTheSameForm(t *testing.T) {
	html := buildBlock(t, Block{Kind: "форма", ID: "addhost", Style: "rows",
		Buttons: []Button{{Name: "Добавить", Action: "add_host"}},
		Fields: []Field{
			{Kind: "text", Name: "name", AnchorID: "addhost-name", Label: "Имя"},
			{Kind: "number", Name: "port", AnchorID: "addhost-port", Label: "Порт", Value: "22", More: true},
			{Kind: "toggle", Name: "sudo", AnchorID: "addhost-sudo", Label: "Разрешить sudo", More: true},
		}})
	if strings.Count(html, "<form") != 1 {
		t.Fatalf("форм не одна:\n%s", html)
	}
	at := strings.Index(html, `<details class="more-fields">`)
	if at < 0 || !strings.Contains(html, "Ещё 2 поля") {
		t.Fatalf("нет «Ещё 2 поля»:\n%s", html)
	}
	if strings.Index(html, `name="name"`) > at || strings.Index(html, `name="port"`) < at || strings.Index(html, `name="sudo"`) < at {
		t.Errorf("обычное поле должно стоять над сворачиваемым, редкие — внутри:\n%s", html)
	}
	if strings.Index(html, "</details>") > strings.Index(html, "</form>") {
		t.Error("«Ещё» вышло за форму: поля не уйдут вместе с ней")
	}
	// без редких полей «Ещё» нет
	none := buildBlock(t, Block{Kind: "форма", ID: "f", Fields: []Field{{Kind: "text", Name: "a", AnchorID: "f-a", Label: "А"}}})
	if strings.Contains(none, "more-fields") {
		t.Error("«Ещё» без редких полей")
	}
	for n, want := range map[int]string{1: "1 поле", 2: "2 поля", 5: "5 полей", 11: "11 полей", 21: "21 поле", 22: "22 поля"} {
		if got := moreWord(n); got != want {
			t.Errorf("moreWord(%d) = %q, ждали %q", n, got, want)
		}
	}
}

// «Опасное» — раздел со Style «danger»: красная карточка, кнопка внутри видна.
func TestDangerSectionHasItsOwnClass(t *testing.T) {
	html := buildBlock(t, Block{Kind: "раздел", ID: "host-danger", Title: "Опасное", Style: "danger",
		Children: []Block{{Kind: "строка", Name: "Удалить сервер", Buttons: []Button{{Name: "Удалить…", Action: "delete_host", Target: "x", Kind: "destructive"}}}}})
	if !strings.Contains(html, `class="section  danger"`) {
		t.Errorf("у раздела нет класса danger:\n%s", html)
	}
	if !strings.Contains(html, `class="action destructive"`) {
		t.Errorf("кнопка «Удалить…» не видна в карточке:\n%s", html)
	}
}

// Полка «ждут вас»: кнопка в заголовке, меню у строки и вложенная группа
// одинаковых команд.
func TestShelfHeaderButtonsRowMenuAndGroups(t *testing.T) {
	html := buildBlock(t, Block{Kind: "полка", Title: "Ждут вас", Meta: "3 команды",
		Buttons: []Button{{Name: "Снять все с очереди", Action: "reject_all_ssh", Kind: "destructive"}},
		Children: []Block{
			{Kind: "строка", Name: "df -h", Caption: "srv-vpn", Buttons: []Button{{Name: "Выполнить", Action: "approve_ssh", Target: "1", Kind: "primary"}},
				Menu: []Button{{Name: "Выполнить и разрешить впредь", Action: "approve_ssh_read", Target: "1"}}},
			{Kind: "группа-строк", ID: "same", Name: "sudo apt-get upgrade -y", Children: []Block{
				{Kind: "строка", Name: "srv-vpn", Buttons: []Button{{Name: "Выполнить", Action: "approve_ssh", Target: "2"}}}}},
		}})
	for _, want := range []string{
		`<span class="shelf-actions">`, "Снять все с очереди",
		`class="row-menu"`, "Выполнить и разрешить впредь",
		`<details class="row-group"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("в полке нет %q:\n%s", want, html)
		}
	}
	// группа не рисуется строкой полки: у неё свои раскрытие и кнопки внутри
	if strings.Count(html, `class="shelf-row row`) != 1 {
		t.Errorf("строк полки не одна:\n%s", html)
	}
}

// Подсвеченное пояснение над настройками («Одинаково для всех ящиков»): жирное
// начало и обычный текст следом, а не абзац, неотличимый от остальных.
func TestCalloutTextHasBoldLead(t *testing.T) {
	html := buildBlock(t, Block{Kind: "текст", Style: "callout", Title: "Одинаково для всех ящиков.", Text: "Меняется сразу у всех."})
	for _, want := range []string{`class="callout"`, `<b>Одинаково для всех ящиков.</b>`, `Меняется сразу у всех.`} {
		if !strings.Contains(html, want) {
			t.Errorf("в разметке пояснения нет %q:\n%s", want, html)
		}
	}
	// без начала — просто плашка с текстом
	plain := buildBlock(t, Block{Kind: "текст", Style: "callout", Text: "Только текст."})
	if !strings.Contains(plain, `class="callout">Только текст.`) || strings.Contains(plain, "<b>") {
		t.Errorf("плашка без начала:\n%s", plain)
	}
	// обычный текст остаётся абзацем
	if p := buildBlock(t, Block{Kind: "текст", Text: "Абзац."}); !strings.Contains(p, `class="paragraph"`) || strings.Contains(p, "callout") {
		t.Errorf("обычный текст:\n%s", p)
	}
}

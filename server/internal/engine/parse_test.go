package engine

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Разбор ответов модели: блоки, таймеры, шапки скиллов. Модель пишет
// свободный текст, поэтому здесь важнее живучесть, чем красота случаев.

func TestParseTimerDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"10 мин", 10 * time.Minute, true},
		{"через 2 часа", 2 * time.Hour, true},
		{"90 секунд", 90 * time.Second, true},
		{"1 ч", time.Hour, true},
		{"3 дня", 72 * time.Hour, true},
		{"5", 5 * time.Minute, true},
		{"30s", 30 * time.Second, true},
		{"скоро", 0, false},
		{"0 мин", 0, false},
		{"", 0, false},
		{"10000000000", 0, false}, // переполнение: срок ушёл бы в прошлое
		{"400 дней", 0, false},    // дальше предела здравого смысла
	}
	for _, c := range cases {
		got, ok := parseTimerDuration(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseTimerDuration(%q) = %v, %v; хотели %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Блок модуля с вложенными ограждениями: первое же ``` — не конец блока.
func TestExtractModuleBlockNested(t *testing.T) {
	answer := "Вот модуль.\n\n```sema-module:курсы\n```json\n{\"name\": \"курсы\"}\n```\n```javascript\nfunction run() {}\n```\n```\n\nГотово."
	block, clean, ok := extractModuleBlock(answer)
	if !ok {
		t.Fatal("блок не найден")
	}
	if !strings.Contains(block, "\"name\": \"курсы\"") || !strings.Contains(block, "function run()") {
		t.Errorf("блок обрезан: %q", block)
	}
	if strings.Contains(clean, "sema-module") || !strings.Contains(clean, "Готово.") {
		t.Errorf("текст после вырезания: %q", clean)
	}
}

// Незакрытое ограждение — берём до конца ответа, а не теряем модуль.
func TestExtractModuleBlockUnclosed(t *testing.T) {
	block, clean, ok := extractModuleBlock("Смотри:\n```sema-module\nкод без конца")
	if !ok || !strings.Contains(block, "код без конца") {
		t.Fatalf("незакрытый блок потерян: %q, %v", block, ok)
	}
	if clean != "Смотри:" {
		t.Errorf("остаток текста: %q", clean)
	}
}

// Ответ без блока остаётся ответом целиком.
func TestExtractModuleBlockAbsent(t *testing.T) {
	answer := "Ничего не предлагаю, просто рассказываю про ```json в письмах."
	block, clean, ok := extractModuleBlock(answer)
	if ok || block != "" || clean != answer {
		t.Errorf("нашли блок там, где его нет: %q / %q / %v", block, clean, ok)
	}
}

func TestParseSkillFile(t *testing.T) {
	raw := "---\ntitle: Linux-сервер\ngroup: серверы\nkeywords: apt, systemd\nwhen: когда просят обновить сервер\n---\n\nСначала посмотри журнал.\n"
	k, err := parseSkillFile("linux-admin", raw)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if k.Title != "Linux-сервер" || k.Group != "серверы" || k.Keywords != "apt, systemd" {
		t.Errorf("шапка разобрана неверно: %+v", k)
	}
	if k.Text != "Сначала посмотри журнал." {
		t.Errorf("текст: %q", k.Text)
	}
	if _, err := parseSkillFile("битый", "---\ntitle: без конца\n"); err == nil {
		t.Error("незакрытая шапка принята")
	}
	if _, err := parseSkillFile("пустой", "---\ntitle: Пусто\n---\n\n"); err == nil {
		t.Error("скилл без текста принят")
	}
}

// clip режет по буквам, а не по байтам: русский текст иначе теряется вдвое
// раньше срока и до модели не доезжает.
func TestClipCountsRunes(t *testing.T) {
	s := strings.Repeat("я", 100)
	got := clip(s, 40)
	if utf8.RuneCountInString(got) != 40 {
		t.Fatalf("clip дал %d букв", utf8.RuneCountInString(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("clip разрезал букву посередине")
	}
	if clip("коротко", 100) != "коротко" {
		t.Error("короткая строка изменена")
	}
}

// Фаззинг разбора: любой ответ модели разбирается без паники, а вырезанный
// блок с остатком текста не длиннее исходного ответа.
func FuzzExtractModuleBlock(f *testing.F) {
	f.Add("```sema-module\n{}\n```")
	f.Add("```sema-module:имя\n```json\n{}\n```\n```")
	f.Add("текст без блока")
	f.Add("")
	f.Fuzz(func(t *testing.T, answer string) {
		block, clean, ok := extractModuleBlock(answer)
		if !ok {
			if clean != answer {
				t.Fatalf("без блока текст изменён: %q против %q", clean, answer)
			}
			return
		}
		if utf8.RuneCountInString(block)+utf8.RuneCountInString(clean) > utf8.RuneCountInString(answer) {
			t.Fatalf("разбор родил текст: блок %d + остаток %d > ответ %d",
				utf8.RuneCountInString(block), utf8.RuneCountInString(clean), utf8.RuneCountInString(answer))
		}
	})
}

// Фаззинг таймеров: разбор не паникует, а принятый срок всегда положительный
// и не уводит задачу на годы вперёд.
func FuzzParseTimerDuration(f *testing.F) {
	f.Add("10 мин")
	f.Add("через 2 часа")
	f.Add("0")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		d, ok := parseTimerDuration(s)
		if !ok {
			return
		}
		if d <= 0 || d > timerMaxDelay {
			t.Fatalf("принят срок %v для %q", d, s)
		}
	})
}

// В телеграм-чате написанное уходит собеседнику; Сёму зовут по имени.
func TestAsksSema(t *testing.T) {
	e := &Engine{}
	cases := map[string]bool{
		"Сёма, посмотри что там":    true,
		"Сема поддержи разговор":    true,
		"СЁМА: что там":             true,
		"сёма помоги":               true,
		"Сёма":                      true,
		"привет, как дела":          false,
		"Сёмашка передавала привет": false,
		"скажи сёме спасибо":        false,
	}
	for text, want := range cases {
		if got := e.asksSemaWord(text, "сёма"); got != want {
			t.Errorf("%q: получили %v, ждали %v", text, got, want)
		}
	}
}

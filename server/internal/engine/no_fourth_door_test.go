package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Правило «секретное не уходит недоверенной модели» дважды ломалось
// одинаково: появлялся новый путь к модели, а проверку рядом поставить
// забывали — так утекали разметка писем, картинки и эмбеддинги.
//
// Тест держал это правило плохо. Он засчитывал за проверку **комментарий**
// `// наружу не уходит: …` — то есть обещание автора, которое ничего не
// исполняет, — и вдобавок пропускал семь файлов целиком, включая те, где
// двери и живут. Зелёный на пустом месте хуже красного: он говорит, что
// правило держится, когда его не держит никто.
//
// Теперь правило держится по построению, и тест проверяет само построение:
//
//  1. карта провайдеров закрыта, и единственный путь к модели —
//     `provider()`, который заворачивает её в `дверь`;
//  2. провайдер строится только там, где его тут же кладут в эту карту;
//  3. пути мимо провайдера (эмбеддер) стоят рядом с настоящей проверкой —
//     вызовом функции, а не обещанием в комментарии.
//
// Файлы не исключаются: двери проверяют сами себя, и это правильно —
// именно в них ошибка стоит дороже всего.

// gateFuncs — функции, которые и есть двери. Внутри них вызов модели —
// не обход правила, а его исполнение. Список нарочно короткий и явный:
// новая дверь добавляется сюда руками, и это видно в разборе изменения.
var gateFuncs = map[string]bool{
	"provider":       true, // заворачивает провайдера в дверь
	"Complete":       true, // сама дверь
	"CompleteStream": true,
	"CompleteImage":  true,
	"Transcribe":     true, // речь: тоже через дверь, см. speechOnlyThroughGate
	"check":          true,
	"AskRole":        true, // дверь роли: внутри `provider()`
	"AskRoleSpare":   true,
	"askRoleGuarded": true,
	"askRoleClosed":  true,
	"askMemory":      true,
	"embedIfAllowed": true, // дверь эмбеддера
	"mayEmbed":       true,
}

func TestNoFourthGate(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sources[f] = string(b)
	}

	gateOneForAll(t, sources)
	providerNotBuiltBypassingMap(t, sources)
	bypassProviderOnlyWithCheck(t, sources)
	speechOnlyThroughGate(t, sources)
}

// speechOnlyThroughGate — звук уходит провайдеру только через дверь. Аудио
// классу не поддаётся, и дверь смотрит на голос (чей он и что решил человек
// про `speech.outside`); вызвать `.Transcribe(` на чём-то, что дверью не
// обёрнуто, значит отдать чужую запись облачной модели молча. Провайдер
// достаётся только через `provider()`, а он всегда отдаёт обёртку, — поэтому
// прямой вызов допустим лишь внутри самой двери (`gate.Transcribe`) и в
// `llm.Hear`, который живёт в другом пакете и провайдера сам не берёт.
func speechOnlyThroughGate(t *testing.T, sources map[string]string) {
	t.Helper()
	direct := regexp.MustCompile(`\.Transcribe\(`)
	for f, src := range sources {
		for i, s := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(s), "//") || !direct.MatchString(s) {
				continue
			}
			if gateFuncs[funcAtLine(src, i)] {
				continue
			}
			t.Errorf("%s:%d вызывает Transcribe мимо двери (в %s): %s",
				f, i+1, funcAtLine(src, i), strings.TrimSpace(s))
		}
	}
	// и сама дверь обязана это умение объявлять: без метода у обёртки
	// llm.Hear не увидит Transcriber и молча решит, что модель речь не понимает
	g, ok := sources["gate.go"]
	if !ok {
		t.Fatal("gate.go не прочитался")
	}
	if !strings.Contains(g, "func (d *gate) Transcribe(") {
		t.Fatal("у двери нет Transcribe — речь ушла бы мимо неё")
	}
	if !strings.Contains(funcBody(g, "func (d *gate) Transcribe("), "speechAllowed(") {
		t.Fatal("gate.Transcribe не спрашивает speechAllowed — голосовое чужого уйдёт облачной модели")
	}
}

// gateOneForAll — карту провайдеров читают только её четыре хранителя, а
// `provider()` отдаёт наружу обёртку. Пока это так, любой `llm.Provider` в
// пакете уже прошёл дверь, и отдельная проверка у каждого вызова не нужна.
func gateOneForAll(t *testing.T, sources map[string]string) {
	t.Helper()
	mapping := regexp.MustCompile(`e\.providers\[`)
	for f, src := range sources {
		for i, s := range strings.Split(src, "\n") {
			if !mapping.MatchString(s) {
				continue
			}
			if fn := funcAtLine(src, i); !map[string]bool{
				"provider": true, "hasProvider": true, "setProvider": true, "dropProvider": true,
			}[fn] {
				t.Errorf("%s:%d читает карту провайдеров мимо хранителей (в %s): %s",
					f, i+1, fn, strings.TrimSpace(s))
			}
		}
	}
	src, ok := sources["engine.go"]
	if !ok {
		t.Fatal("engine.go не прочитался — проверять нечего")
	}
	body := funcBody(src, "func (e *Engine) provider(")
	if body == "" {
		t.Fatal("функции provider() больше нет: дверь переехала, поправьте тест вместе с ней")
	}
	if !strings.Contains(body, "&gate{") {
		t.Fatal("provider() отдаёт модель без двери — значит двери нет вовсе")
	}
}

// providerNotBuiltBypassingMap — собранный провайдер обязан лечь в карту
// (или уйти вызывающему, который её и заполняет). Собрать провайдера и
// позвать его на месте значит обойти дверь, не написав ни строчки про неё.
func providerNotBuiltBypassingMap(t *testing.T, sources map[string]string) {
	t.Helper()
	build := regexp.MustCompile(`llm\.Build\(`)
	for f, src := range sources {
		line := strings.Split(src, "\n")
		for i, s := range line {
			if !build.MatchString(s) {
				continue
			}
			to := i + 12
			if to > len(line) {
				to = len(line)
			}
			nearby := strings.Join(line[i:to], "\n")
			// либо кладём в карту (в том числе в ту, из которой её соберут
			// при запуске), либо возвращаем тому, кто положит
			if strings.Contains(nearby, "setProvider(") || strings.Contains(nearby, "providers[name] = p") ||
				strings.Contains(line[i], "return llm.Build(") {
				continue
			}
			t.Errorf("%s:%d строит провайдера и никуда не кладёт — он пойдёт мимо двери: %s",
				f, i+1, strings.TrimSpace(s))
		}
	}
}

// bypassProviderOnlyWithCheck — эмбеддер не `llm.Provider`, дверь его
// не оборачивает, и текст уходит ему тем же путём, что письмо модели.
// Значит рядом должна стоять настоящая проверка: вызов функции, а не
// комментарий. Комментарий здесь не засчитывается нарочно — он ничего не
// исполняет, а выглядит так же успокаивающе, как код.
func bypassProviderOnlyWithCheck(t *testing.T, sources map[string]string) {
	t.Helper()
	calls := regexp.MustCompile(`e\.embedder\.(Embed|EmbedOne)\(`)
	guard := regexp.MustCompile(`embedIfAllowed\(|mayEmbed\(|outsideCut\(|BeforeModel\(|EmbedTrusted\(`)
	var bare []string
	for f, src := range sources {
		line := strings.Split(src, "\n")
		for i, s := range line {
			if !calls.MatchString(s) {
				continue
			}
			if gateFuncs[funcAtLine(src, i)] {
				continue
			}
			from := i - 12
			if from < 0 {
				from = 0
			}
			if guard.MatchString(strings.Join(line[from:i+1], "\n")) {
				continue
			}
			bare = append(bare, f+":"+itoa(i+1)+" → "+strings.TrimSpace(s))
		}
	}
	if len(bare) > 0 {
		t.Fatalf("текст уходит эмбеддеру мимо проверки класса (%d):\n  %s\n\n"+
			"Позовите embedIfAllowed или mayEmbed. Комментарий «наружу не уходит» здесь\n"+
			"не считается: он ничего не проверяет, а выглядит как проверка.",
			len(bare), strings.Join(bare, "\n  "))
	}
}

// funcAtLine — имя функции, внутри которой стоит строка. Нужно, чтобы
// отличить дверь от того, кто в неё ходит.
func funcAtLine(src string, number int) string {
	line := strings.Split(src, "\n")
	for i := number; i >= 0; i-- {
		if !strings.HasPrefix(line[i], "func ") {
			continue
		}
		s := line[i]
		if j := strings.Index(s, ")"); strings.HasPrefix(s, "func (") && j > 0 {
			s = s[j+1:]
		} else {
			s = strings.TrimPrefix(s, "func")
		}
		s = strings.TrimSpace(s)
		if j := strings.IndexAny(s, "([{"); j > 0 {
			s = s[:j]
		}
		return strings.TrimSpace(s)
	}
	return ""
}

// funcBody — текст функции от её объявления до закрывающей скобки в
// начале строки. Грубо, но для проверки «внутри есть такая строка» хватает.
func funcBody(src, decl string) string {
	i := strings.Index(src, decl)
	if i < 0 {
		return ""
	}
	end := strings.Index(src[i:], "\n}")
	if end < 0 {
		return src[i:]
	}
	return src[i : i+end]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

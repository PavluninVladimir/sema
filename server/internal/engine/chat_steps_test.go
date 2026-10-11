package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// projects43 — ответ `project_list`, как у Planner 4 октября 2026: 43
// проекта с заметками, около 16 тысяч знаков, нужный — сорок первый.
func projects43() string {
	type proj struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Note  string `json:"note"`
		Color string `json:"color"`
	}
	var list []proj
	for i := 1; i <= 43; i++ {
		title := fmt.Sprintf("Проект номер %d", i)
		if i == 41 {
			title = "Сёма"
		}
		list = append(list, proj{ID: fmt.Sprintf("P-%03d", i), Title: title,
			Note: strings.Repeat("заметка о проекте, длинная и подробная. ", 9), Color: "#aabbcc"})
	}
	raw, _ := json.Marshal(list)
	return "Showing all 43 project items. Items:\n" + string(raw)
}

func TestCompactMCPListAll43Titles(t *testing.T) {
	text := projects43()
	if n := len([]rune(text)); n < 15000 {
		t.Fatalf("ответ короче живого: %d знаков", n)
	}
	short, ok := compactMCPList(text, splitCommaList(mcpListFieldsDefault), 6000, []string{"maxCount"})
	if !ok {
		t.Fatal("список не узнан")
	}
	if n := len([]rune(short)); n > 6000 {
		t.Errorf("выжимка длиннее предела: %d", n)
	}
	for i := 1; i <= 43; i++ {
		want := fmt.Sprintf("id: P-%03d", i)
		if !strings.Contains(short, want) {
			t.Errorf("в выжимке нет %s", want)
		}
	}
	for _, want := range []string{"title: Сёма", "Showing all 43 project items", "всего 43 записей", "maxCount"} {
		if !strings.Contains(short, want) {
			t.Errorf("в выжимке нет %q", want)
		}
	}
	if strings.Contains(short, "заметка") || strings.Contains(short, "#aabbcc") {
		t.Error("в выжимку попали поля, которых нет в настройке")
	}
}

// Не список и короткий список — как раньше: выжимки нет.
func TestCompactMCPListNotAList(t *testing.T) {
	fields := splitCommaList(mcpListFieldsDefault)
	long := strings.Repeat("просто текст ответа. ", 400)
	for name, text := range map[string]string{
		"текст":              long,
		"одиночный объект":   `{"id":"t1","title":"Задача","note":"` + long + `"}`,
		"массив без записей": `["` + long + `"]`,
		"короткий список":    `[{"id":"t1","title":"Оплатить"}]`,
	} {
		if _, ok := compactMCPList(text, fields, 2000, nil); ok {
			t.Errorf("%s: выжимка там, где её быть не должно", name)
		}
	}
	// и модель получает его как раньше — обрезкой с названным остатком
	e, _ := newTestEngine(t, "", nil)
	got := e.mcpModelText("нет-такого", "x", long, 2000)
	if got != clipNamed(long, 2000) {
		t.Error("не список ушёл модели не прежней обрезкой")
	}
}

// Выжимка, которая сама не влезает, режется по записям, с остатком словами.
func TestCompactMCPListCutByRecords(t *testing.T) {
	var items []string
	for i := 1; i <= 300; i++ {
		items = append(items, fmt.Sprintf(`{"id":"T-%d","title":"Задача с довольно длинным названием номер %d","note":"%s"}`,
			i, i, strings.Repeat("x", 50)))
	}
	text := `{"total":300,"items":[` + strings.Join(items, ",") + `]}`
	short, ok := compactMCPList(text, splitCommaList(mcpListFieldsDefault), 3000, nil)
	if !ok {
		t.Fatal("объект со списком не узнан")
	}
	if n := len([]rune(short)); n > 3000+200 {
		t.Errorf("выжимка %d знаков при пределе 3000", n)
	}
	if !strings.Contains(short, "total: 300") {
		t.Error("простое поле обёртки потерялось")
	}
	if !strings.Contains(short, "не поместились") {
		t.Error("остаток не назван")
	}
	lines := strings.Split(strings.TrimSpace(short), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[len(lines)-1], "…") {
		t.Fatalf("остаток не последней строкой: %q", lines[len(lines)-1])
	}
	last := lines[len(lines)-2]
	var n int
	if _, err := fmt.Sscanf(last, "- id: T-%d", &n); err != nil ||
		!strings.HasSuffix(last, fmt.Sprintf("title: Задача с довольно длинным названием номер %d", n)) {
		t.Errorf("последняя запись перед остатком обрезана: %q", last)
	}
}

// Поля выжимки — настройка: правка доезжает до модели.
func TestMCPListFieldsIsSetting(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting(mcpChatListFieldsKey, "id, color"); err != nil {
		t.Fatal(err)
	}
	got := e.mcpModelText("нет-такого", "project_list", projects43(), 6000)
	if !strings.Contains(got, "color: #aabbcc") || strings.Contains(got, "title: Сёма") {
		t.Errorf("поля выжимки не из настройки: %.300s", got)
	}
}

// Найти, потом завести: модель смотрит список проектов, находит сорок
// первый по выжимке и заводит задачу — та ждёт кнопки, и шаги на этом
// кончаются. Два хода модели за одно обращение человека; до шагов второй ход
// был, но списка в нём модель не видела — нужный проект лежал за обрезкой.
func TestChatStepsFindThenCreate(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Посмотрю проекты.\n```sema-mcp:planner\n{\"tool\": \"project_list\"}\n```",
		"Нашёл проект «Сёма», задача ждёт вашей кнопки.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Починить шаги\", \"projectId\": \"P-041\"}}\n```",
		"Третьего хода быть не должно.")
	f.srv.CallReply = func(name string, _ map[string]any) string {
		if name == "project_list" {
			return projects43()
		}
		return `{"id":"T-1"}`
	}
	f.ask(t, "заведи в Planner задачу «Починить шаги» в проекте Сёма")

	prompts := f.model.Prompts()
	if len(prompts) != 2 {
		t.Fatalf("модель звали %d раз, ждали два хода", len(prompts))
	}
	if !strings.Contains(prompts[1], "title: Сёма") || !strings.Contains(prompts[1], "id: P-041") {
		t.Error("во втором ходе модель не видит сорок первого проекта")
	}
	if !strings.Contains(prompts[1], "up to 2 more turns") {
		t.Error("во втором ходе нет подсказки об оставшихся шагах")
	}
	if strings.Contains(prompts[1], "заметка о проекте") {
		t.Error("модель получила полный список вместо выжимки")
	}
	names := []string{}
	for _, c := range f.srv.Calls() {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "project_list" {
		t.Fatalf("на сервер ушло: %v (task_create должен ждать кнопки)", names)
	}
	statuses := []string{}
	for _, c := range f.cards(t) {
		statuses = append(statuses, c.MCP.Tool+":"+c.MCP.Status)
	}
	if got := strings.Join(statuses, " "); got != "project_list:executed task_create:pending" {
		t.Fatalf("карточки: %s", got)
	}
	// карточка у человека — полный ответ (в пределе карточки), а не выжимка
	if c := f.cards(t)[0]; !strings.Contains(c.Code, "заметка о проекте") {
		t.Error("в карточке выжимка вместо ответа сервера")
	}
	if got := f.lastAnswer(t); !strings.Contains(got, "Нашёл проект «Сёма»") || strings.Contains(got, "sema-mcp") {
		t.Errorf("итог: %q", f.lastAnswer(t))
	}
}

// Модель на каждом ходу зовёт новый читающий инструмент: шагов не больше
// предела, на последнем — прежнее правило «результаты — карточками».
func TestChatStepsLimit(t *testing.T) {
	var answers []string
	for i := 1; i <= 8; i++ {
		answers = append(answers, fmt.Sprintf("Шаг %d.\n```sema-mcp:planner\n{\"tool\": \"task_list\", \"args\": {\"maxCount\": %d}}\n```", i, i))
	}
	f := newMCPChatFix(t, true, answers...)
	if err := f.st.SetSetting(chatReadStepsKey, "3"); err != nil {
		t.Fatal(err)
	}
	f.ask(t, "какие задачи в Planner")

	prompts := f.model.Prompts()
	if len(prompts) != 3 {
		t.Fatalf("модель звали %d раз при пределе 3", len(prompts))
	}
	if !strings.Contains(prompts[2], "shown to the user as cards") {
		t.Error("на последнем ходу нет прежнего правила про карточки")
	}
	if n := len(f.srv.Calls()); n != 3 {
		t.Errorf("вызовов на сервере %d, ждали три — по одному на ход", n)
	}
	if strings.Contains(f.lastAnswer(t), "sema-mcp") {
		t.Error("блок последнего хода остался в тексте")
	}
}

// Тот же вызов второй раз в одном обращении не исполняется: сервер получил
// один вызов, модель — прежний результат с пометкой.
func TestChatStepsSameCallOnce(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Проверю проекты.\n```sema-mcp:planner\n{\"tool\": \"project_list\"}\n```",
		"Ещё раз проверю проекты.\n```sema-mcp:planner\n{\"tool\": \"project_list\", \"args\": {}}\n```",
		"Проектов много, нужный — «Сёма».")
	f.srv.CallReply = func(string, map[string]any) string { return projects43() }
	f.ask(t, "найди проект Сёма в Planner")

	if n := len(f.srv.Calls()); n != 1 {
		t.Fatalf("сервер получил %d вызовов project_list, ждали один", n)
	}
	prompts := f.model.Prompts()
	if len(prompts) != 3 {
		t.Fatalf("модель звали %d раз", len(prompts))
	}
	if !strings.Contains(prompts[2], "already made in this answer") || !strings.Contains(prompts[2], "title: Сёма") {
		t.Error("модель не получила прежний результат с пометкой о повторе")
	}
	cards := f.cards(t)
	if len(cards) != 1 {
		t.Errorf("карточек %d: повтор не должен ложиться второй карточкой в переписку", len(cards))
	}
	if !strings.Contains(f.lastAnswer(t), "нужный — «Сёма»") {
		t.Errorf("итог: %q", f.lastAnswer(t))
	}
}

// Ключ вызова не зависит от порядка аргументов и регистра имени сервера.
func TestCallKeyStable(t *testing.T) {
	a := callKey("Planner", "task_list", map[string]any{"projectId": "P", "maxCount": 5})
	b := callKey("planner", "task_list", map[string]any{"maxCount": 5, "projectId": "P"})
	if a != b {
		t.Error("один и тот же вызов дал разные ключи")
	}
	if callKey("s", "project_list", nil) != callKey("s", "project_list", map[string]any{}) {
		t.Error("пустые аргументы и их отсутствие — разные ключи")
	}
	if a == callKey("planner", "task_list", map[string]any{"projectId": "Q", "maxCount": 5}) {
		t.Error("разные аргументы — один ключ")
	}
}

// «Хорошо, сделаю» без блока человеку не уходит: модель получает напоминание
// и отвечает делом.
func TestChatPromiseReminded(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Хорошо, сделаю настройку модуля.",
		"Смотрю задачи.\n```sema-mcp:planner\n{\"tool\": \"task_list_today\"}\n```",
		"Сегодня две задачи.")
	f.ask(t, "настрой модуль задач")

	prompts := f.model.Prompts()
	if len(prompts) != 3 {
		t.Fatalf("модель звали %d раз", len(prompts))
	}
	if !strings.Contains(prompts[1], "promised an action but contains no block") {
		t.Error("второй ход без напоминания")
	}
	if n := len(f.srv.Calls()); n != 1 {
		t.Errorf("после напоминания дело не сделано: вызовов %d", n)
	}
	if got := f.lastAnswer(t); strings.Contains(got, "сделаю") || !strings.Contains(got, "две задачи") {
		t.Errorf("человеку ушло: %q", got)
	}
}

// Честный вопрос без слов обещания уходит человеку сразу.
func TestChatQuestionGoesStraight(t *testing.T) {
	f := newMCPChatFix(t, true, "В каком проекте завести задачу?")
	f.ask(t, "заведи задачу")
	if n := len(f.model.Prompts()); n != 1 {
		t.Fatalf("модель звали %d раз, ждали один", n)
	}
	if f.lastAnswer(t) != "В каком проекте завести задачу?" {
		t.Errorf("итог: %q", f.lastAnswer(t))
	}
}

// Два обещания подряд: второе уходит человеку, третьего хода нет.
func TestChatPromiseTwiceGoesToHuman(t *testing.T) {
	f := newMCPChatFix(t, true, "Сейчас проверю.", "Хорошо, займусь этим чуть позже.")
	f.ask(t, "проверь задачи")
	if n := len(f.model.Prompts()); n != 2 {
		t.Fatalf("модель звали %d раз, ждали два", n)
	}
	if got := f.lastAnswer(t); got != "Хорошо, займусь этим чуть позже." {
		t.Errorf("итог: %q", got)
	}
}

// Приметы обещания — по началу слова, без регистра и без различия «е» и «ё»;
// справочник — настройка.
func TestIsPromise(t *testing.T) {
	m := splitCommaList(promiseMarkersDefault)
	for text, want := range map[string]bool{
		"Хорошо, сделаю.":           true,
		"ПРОВЕРЮ список":            true,
		"Сейчас посмотрю, что там.": true,
		"Займусь.":                  true,
		"Изучу клиент и документацию, затем соберу модуль": true, // живой ответ 4 октября 2026, 11:11
		"Какой проект выбрать?":                            false,
		"Сделано: задача заведена.":                        false,
	} {
		if got := isPromise(text, m); got != want {
			t.Errorf("isPromise(%q) = %v, ждали %v", text, got, want)
		}
	}
	if isPromise("Переделаю завтра", []string{"делаю"}) {
		t.Error("примета сработала внутри слова")
	}
	if !isPromise("Всё учтём", []string{"учтем"}) {
		t.Error("«ё» и «е» различаются")
	}
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting(chatPromiseMarkersKey, "обещаю, ладно"); err != nil {
		t.Fatal(err)
	}
	if got := e.promiseMarkers(); strings.Join(got, "|") != "обещаю|ладно" {
		t.Errorf("приметы не из настройки: %v", got)
	}
}

// «Отправлено на подтверждение» без единого блока — ничего не случилось:
// модель получает напоминание и делает вызов по-настоящему. 4 октября 2026
// так «завёлся» баг в Planner, которого не было.
func TestChatClaimWithoutBlockReminded(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Создание бага в Planner отправлено на подтверждение.",
		"Завожу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Баг: меню наезжает\"}}\n```")
	f.ask(t, "заведи баг в Planner")
	if n := len(f.model.Prompts()); n != 2 || !strings.Contains(f.model.Prompts()[1], claimSign) {
		t.Fatalf("модель звали %d раз, напоминания нет", n)
	}
	pend, _ := f.st.MCPPendingList()
	if len(pend) != 1 || pend[0].Tool != "task_create" {
		t.Fatalf("вызов так и не случился: %+v", pend)
	}
}

// Два живых обхода той же проверки, вечер 4 октября 2026 (ветка 103):
// модель нашла скриптом фото и в том же ответе написала, что баг ушёл на
// подтверждение; потом — «отправил в Planner на подтверждение», со
// вставкой посреди приметы. Ни вызова, ни кнопки не было оба раза.
func TestChatClaimAfterOnlyAScriptReminded(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Ищу фото.\n```sema:Найти фото бага\nreturn 1;\n```",
		"Создание бага отправлено на подтверждение. Фото не подтверждает дефект.",
		"Завожу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Меню закрывается при прокрутке\"}}\n```")
	f.ask(t, "заведи баг про меню")
	prompts := f.model.Prompts()
	if len(prompts) != 3 || !strings.Contains(prompts[2], claimSign) {
		t.Fatalf("модель звали %d раз, напоминания после скрипта нет", len(prompts))
	}
	if pend, _ := f.st.MCPPendingList(); len(pend) != 1 || pend[0].Tool != "task_create" {
		t.Fatalf("вызов так и не случился: %+v", pend)
	}
}

func TestChatClaimWithWordsInsideMarkerReminded(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Задачу «Исправить закрытие меню при прокрутке» отправил в Planner на подтверждение.",
		"Завожу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Меню закрывается при прокрутке\"}}\n```")
	f.ask(t, "заведи просто задачку в планировщик")
	if n := len(f.model.Prompts()); n != 2 || !strings.Contains(f.model.Prompts()[1], claimSign) {
		t.Fatalf("модель звали %d раз, напоминания нет", n)
	}
}

// Примета с разрывом: части по порядку, первая с начала слова, разрыв не
// длиннее markerGap — фраза через предложение не в счёт.
func TestGapMarker(t *testing.T) {
	m := splitCommaList(claimMarkersDefault)
	for text, want := range map[string]bool{
		"Задачу отправил в Planner на подтверждение.": true,
		"Отправлено на подтверждение.":                true,
		"Создал в проекте «Сёма» задачу «Меню».":      true,
		"Переотправил на подтверждение":               false,
		"Отправил письмо. Потом долго думал, что делать дальше, и только потом, наконец, на подтверждение": false,
		"Создать задачу?": false,
	} {
		if got := isPromise(text, m); got != want {
			t.Errorf("isPromise(%q) = %v, ждали %v", text, got, want)
		}
	}
}

// Живые модели кончают ответ переводом строки — проверка от этого не
// выключается (ревью 4 октября 2026: findRules обрезает края всегда, и
// сравнение «до/после» принимало это за предложение правила).
func TestChatClaimWithTrailingNewlineReminded(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Задачу «Меню» отправил в Planner на подтверждение.\n",
		"Завожу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Меню\"}}\n```")
	f.ask(t, "заведи задачку")
	if n := len(f.model.Prompts()); n != 2 || !strings.Contains(f.model.Prompts()[1], claimSign) {
		t.Fatalf("модель звали %d раз, напоминания нет", n)
	}
}

// Прочитал список — не значит создал: после читающего вызова «Создал задачу»
// без вызова создания получает напоминание.
func TestChatClaimAfterReadOnlyCallReminded(t *testing.T) {
	f := newMCPChatFix(t, true,
		"```sema-mcp:planner\n{\"tool\": \"task_list\", \"args\": {}}\n```",
		"Создал задачу «Меню» в Planner.",
		"Завожу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Меню\"}}\n```")
	f.ask(t, "заведи задачку")
	prompts := f.model.Prompts()
	if len(prompts) != 3 || !strings.Contains(prompts[2], claimSign) {
		t.Fatalf("модель звали %d раз, напоминания после чтения нет", len(prompts))
	}
}

// Примета из одних разрывов ничего не ловит.
func TestGapMarkerEmpty(t *testing.T) {
	if isPromise("Хорошего дня!", []string{"…"}) || isPromise("Хорошего дня!", []string{" ... "}) {
		t.Error("примета «…» поймала обычный ответ")
	}
}

// claimSign — кусок напоминания о «сделано без дела»: слова «nothing happened»
// стоят и в обычном промпте MCP, и по ним напоминание не отличить.
const claimSign = "tell the user plainly that it was not done"

// Повтор упавшего изменяющего вызова — не дело: после двух отказов «Создал
// задачу» получает напоминание (ревью 4 октября 2026: карточка повтора
// всегда была «выполнен»).
func TestChatClaimAfterFailedThenRepeatedCallReminded(t *testing.T) {
	f := newMCPChatFix(t, true,
		"Завожу.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Меню\"}}\n```",
		"Ещё раз.\n```sema-mcp:planner\n{\"tool\": \"task_create\", \"args\": {\"title\": \"Меню\"}}\n```",
		"Создал задачу «Меню» в Planner.",
		"Не вышло: сервер отказал.")
	f.srv.CallResult = func(name string, args map[string]any) (string, bool) { return "server refused: boom", true }
	// изменяющий вызов без правила ждал бы кнопки — разрешаем, чтобы он исполнился и упал
	if err := f.st.SetMCPRules("planner", map[string]string{"task_create": PolicyAllow}, PolicyAsk); err != nil {
		t.Fatal(err)
	}
	f.ask(t, "заведи задачку в планировщик")
	prompts := f.model.Prompts()
	if len(prompts) != 4 || !strings.Contains(prompts[3], claimSign) {
		t.Fatalf("модель звали %d раз, напоминания после повтора упавшего вызова нет", len(prompts))
	}
}

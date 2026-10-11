package engine

// Скрипты из чата. Настоящий вызов функций у локальных моделей работает
// ненадёжно, а у владельца основная модель как раз локальная. Поэтому
// договор простой: нужна выборка или расчёт — модель пишет в ответе блок
//
//	```sema:Название скрипта
//	   ...код...
//	```
//
// Сёма перехватывает блок, сохраняет его как обычный скрипт, выполняет в
// той же песочнице и подставляет результат обратно в разговор. Скрипт при
// этом остаётся видимым: он появляется в разделе «Скриптов», а в переписке
// висит карточка с кодом и результатом — ничего не выполняется втихую.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

const (
	chatScriptsKey = "chat_scripts"
	maxScriptRuns  = 2 // больше двух запусков на один вопрос — уже цикл
)

// ChatScriptsEnabled — разрешено ли Сёме писать и запускать скрипты в чате.
func (e *Engine) ChatScriptsEnabled() bool {
	return e.store.Setting(chatScriptsKey, "1") == "1"
}

func (e *Engine) SetChatScriptsEnabled(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting(chatScriptsKey, v)
}

// scriptBlock — блок кода, который модель просит выполнить.
var scriptBlock = regexp.MustCompile("(?s)```sema:([^\n]*)\n(.*?)```")

// moduleBlock — модуль, который модель предлагает установить. Он не
// выполняется сразу: человек сначала видит права.
var moduleBlock = regexp.MustCompile("(?s)```sema-module\n(.*?)```")

// ruleBlock — правило, которое Сёма предлагает дописать в инструкцию модуля.
// Само оно ничего не меняет: человек подтверждает кнопкой в переписке.
var ruleBlock = regexp.MustCompile("(?s)```sema-rule:([^\n]*)\n(.*?)```")

// findRules вынимает предложенные правила и кладёт их карточками в чат.
func (e *Engine) findRules(chatID int64, answer string) string {
	for _, m := range ruleBlock.FindAllStringSubmatch(answer, 3) {
		module := strings.TrimSpace(m[1])
		rule := strings.TrimSpace(m[2])
		if module == "" || rule == "" {
			continue
		}
		if _, ok := e.Module(module); !ok {
			module = selfModule // незнакомый модуль — правило общее
		}
		_ = e.saveToolCard(chatID, ToolCard{
			Title:  "Запомнить правило · " + module,
			Code:   rule,
			Result: "предложение: подтвердите — и правило попадёт в инструкцию модуля",
			Rule:   module,
		})
	}
	return strings.TrimSpace(ruleBlock.ReplaceAllString(answer, ""))
}

// ProposedModule — модуль, предложенный Сёмой в переписке.
type ProposedModule struct {
	Module *Module
	Err    string
}

// findModule ищет в ответе модели предложение модуля.
func (e *Engine) findModule(answer string) (string, *ProposedModule) {
	var (
		mod   *Module
		err   error
		clean string
	)
	if name, patch, rest, ok := extractModulePatch(answer); ok {
		// правка по функциям: большой модуль целиком в ответ не влезает
		clean = rest
		if mod, _, err = e.moduleByPatch(name, patch); err != nil {
			log.Printf("правка модуля «%s» не наложилась: %v", name, err)
			return clean, &ProposedModule{Err: "правка не наложилась: " + err.Error()}
		}
	} else {
		block, rest, ok := extractModuleBlock(answer)
		if !ok {
			return answer, nil
		}
		clean = rest
		if mod, err = ParseModule(block); err != nil {
			log.Printf("модуль из чата не разобран: %v; блок: %s", err, clip(block, 800))
			return clean, &ProposedModule{Err: err.Error()}
		}
	}
	// «ssh» в needs без списка — все известные серверы; выдаёт их человек
	if len(mod.SSH) == 1 && mod.SSH[0] == "*" {
		mod.SSH = e.HostNames()
	}
	// разбор кода до предложения: синтаксис и права проверяет Сёма сам,
	// смысловые замечания спрашивает у агента машины, если он на связи.
	// Всё найденное вернётся модели вторым ходом, а не человеку.
	if check, err := e.CheckModuleCode(mod); err != nil {
		return clean, &ProposedModule{Err: err.Error()}
	} else if problem := check.Problem(); problem != "" {
		return clean, &ProposedModule{Err: problem}
	}
	if notes, asked := e.Diagnose(context.Background(), mod); asked && notes != "" {
		return clean, &ProposedModule{Err: "языковой сервер нашёл в коде: " + clip(notes, 600)}
	}
	return clean, &ProposedModule{Module: mod}
}

// extractModuleBlock вынимает блок ```sema-module с учётом вложенных
// ограждений: модели часто кладут внутрь ```json с манифестом и
// ```javascript с кодом, и первое же ``` тогда — не конец блока.
// Ограждение с языком открывает вложенный уровень, голое ``` закрывает.
func extractModuleBlock(answer string) (block, clean string, ok bool) {
	lines := strings.Split(answer, "\n")
	start := -1
	depth := 0
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if start < 0 {
			// модель пишет и ```sema-module, и ```sema-module:<имя> — имя берётся из манифеста
			if t == "```sema-module" || strings.HasPrefix(t, "```sema-module:") {
				start, depth = i, 1
			}
			continue
		}
		switch {
		case strings.HasPrefix(t, "```") && len(t) > 3:
			depth++
		case t == "```":
			depth--
		}
		if depth == 0 {
			block = strings.Join(lines[start+1:i], "\n")
			clean = strings.TrimSpace(strings.Join(append(append([]string{}, lines[:start]...), lines[i+1:]...), "\n"))
			return block, clean, true
		}
	}
	if start >= 0 { // ограждение не закрыто — берём до конца
		return strings.Join(lines[start+1:], "\n"), strings.TrimSpace(strings.Join(lines[:start], "\n")), true
	}
	return "", answer, false
}

// ToolCard — карточка запуска в переписке.
type ToolCard struct {
	Title    string      `json:"title"`
	Code     string      `json:"code"`
	Result   string      `json:"result"`
	Err      string      `json:"err,omitempty"`
	Seconds  float64     `json:"seconds"`
	ScriptID int64       `json:"script_id"`
	Rule     string      `json:"rule,omitempty"`      // карточка-предложение правила для модуля
	RuleDone bool        `json:"rule_done,omitempty"` // правило уже записано в инструкцию
	Memory   *MemoryCard `json:"memory,omitempty"`    // карточка памяти: записано, ждёт, отвергнуто, предложение области
	SSH      *SSHCard    `json:"ssh,omitempty"`       // команда на сервере: выполнена, ждёт, отказ
	Skill    *SkillCard  `json:"skill,omitempty"`     // предложение скилла: разница, принято ли
	Timer    *TimerCard  `json:"timer,omitempty"`     // таймер чата: поставлен, сработал
	Done     *DoneCard   `json:"done,omitempty"`      // вопрос закрыт: итог работы
	TG       *TGCard     `json:"tg,omitempty"`        // сообщение в Telegram: ждёт кнопки, отправлено, отказ
	MCP      *MCPCard    `json:"mcp,omitempty"`       // вызов инструмента чужого сервера: выполнен, ждёт кнопки, отказ
	Assign   *AssignCard `json:"assign,omitempty"`    // поручение: план и ход, нужен секрет, закрыто
	Share    *ShareCard  `json:"share,omitempty"`     // ссылка на файл: действует, отозвана, срок вышел
}

// runChatScripts находит в ответе модели блоки скриптов, выполняет их и
// возвращает текст ответа без блоков плюс карточки запусков.
func (e *Engine) runChatScripts(ctx context.Context, chatID int64, answer string) (string, []ToolCard) {
	if !e.ChatScriptsEnabled() {
		return answer, nil
	}
	blocks := scriptBlock.FindAllStringSubmatch(answer, maxScriptRuns)
	if len(blocks) == 0 {
		return answer, nil
	}
	var cards []ToolCard
	for _, b := range blocks {
		title := strings.TrimSpace(b[1])
		code := strings.TrimSpace(b[2])
		if title == "" {
			title = "Скрипт из чата"
		}
		card := ToolCard{Title: title, Code: code}

		id, err := e.store.CreateScript(title+" · из чата", code)
		if err != nil {
			log.Printf("скрипт из чата: сохранение: %v", err)
		} else {
			card.ScriptID = id
		}
		started := time.Now()
		res := e.RunScript(ctx, code)
		card.Seconds = time.Since(started).Seconds()
		switch {
		case res.Err != nil:
			card.Err = res.ErrText
			if card.Err == "" {
				card.Err = res.Err.Error()
			}
			if res.ErrLine > 0 {
				card.Err = fmt.Sprintf("строка %d: %s", res.ErrLine, card.Err)
			}
		default:
			card.Result = res.Value
			if card.Result == "" {
				card.Result = strings.Join(res.Log, " · ")
			}
			if card.Result == "" {
				card.Result = "выполнен, но ничего не вернул"
			}
		}
		if card.ScriptID != 0 {
			_ = e.store.ScriptRan(card.ScriptID, card.Result, card.Seconds)
		}
		cards = append(cards, card)
		_ = e.saveToolCard(chatID, card)
	}
	return strings.TrimSpace(scriptBlock.ReplaceAllString(answer, "")), cards
}

// saveToolCard кладёт карточку в переписку отдельным сообщением — она
// остаётся в истории чата навсегда, как и всё остальное.
// saveToolCard — карточка в переписку. Пока Сёма отвечает, карточки ждут
// в очереди: человек сначала читает текст, а потом видит подтверждения.
func (e *Engine) saveToolCard(chatID int64, card ToolCard) error {
	e.thinkingMu.Lock()
	thinking := e.thinking[chatID]
	e.thinkingMu.Unlock()
	if thinking {
		e.pendingMu.Lock()
		if e.cardQueue == nil {
			e.cardQueue = map[int64][]ToolCard{}
		}
		e.cardQueue[chatID] = append(e.cardQueue[chatID], card)
		e.pendingMu.Unlock()
		return nil
	}
	return e.writeToolCard(chatID, card)
}

// flushCards кладёт накопленные карточки после ответа Сёмы.
func (e *Engine) flushCards(chatID int64) {
	e.pendingMu.Lock()
	cards := e.cardQueue[chatID]
	delete(e.cardQueue, chatID)
	e.pendingMu.Unlock()
	for _, c := range cards {
		if err := e.writeToolCard(chatID, c); err != nil {
			log.Printf("карточка в чат %d: %v", chatID, err)
		}
	}
}

// replaceSSHCard — обновить карточку команды на месте, если она уже
// стоит в переписке. Возвращает false, когда карточки нет: тогда её
// пишут заново.
func (e *Engine) replaceSSHCard(chatID, pendingID int64, card ToolCard) bool {
	if chatID <= 0 || pendingID <= 0 {
		return false
	}
	msgs, err := e.store.ChatMsgs(chatID, 40)
	if err != nil {
		return false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != "tool" {
			continue
		}
		old, ok := ParseToolCard(m.Content)
		if !ok || old.SSH == nil || old.SSH.PendingID != pendingID {
			continue
		}
		raw, err := json.Marshal(card)
		if err != nil {
			return false
		}
		return e.store.ReplaceChatMsg(m.ID, string(raw)) == nil
	}
	return false
}

func (e *Engine) writeToolCard(chatID int64, card ToolCard) error {
	chatID = e.chatForOutput(chatID) // карточки собеседник не видит — в тред
	raw, err := json.Marshal(card)
	if err != nil {
		return err
	}
	return e.store.AddChatMsg(chatID, "tool", string(raw)) // в тред уже выбран
}

// ParseToolCard разбирает сообщение-карточку для показа на экране.
func ParseToolCard(content string) (ToolCard, bool) {
	var c ToolCard
	// карточка — любой разобранный JSON с заголовком: команда на подтверждение,
	// таймер, скилл и память приходят без кода, и раньше показывались сырым JSON
	if err := json.Unmarshal([]byte(content), &c); err != nil || strings.TrimSpace(c.Title) == "" {
		return ToolCard{}, false
	}
	return c, true
}

// scriptResultsPrompt — что сказать модели после выполнения, чтобы она
// ответила по фактам, а не выдумала числа.
func scriptResultsPrompt(cards []ToolCard) string {
	var b strings.Builder
	b.WriteString("\nScript results (these are real numbers from the user's own data, " +
		"answer using them and do not invent anything):\n")
	for _, c := range cards {
		if c.Err != "" {
			fmt.Fprintf(&b, "- %s: ошибка — %s\n", c.Title, c.Err)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", c.Title, c.Result)
	}
	b.WriteString("Give the final answer in Russian. Do not write another ```sema:``` script block " +
		"(sema-mcp, sema-ssh and the other blocks stay available when the answer needs them).\n")
	return b.String()
}

// chatScriptsPrompt — договор о формате плюс список инструментов, собранный
// из реестра. Раньше список был написан в этой строке руками и расходился с
// тем, что на самом деле умеет песочница.
func (e *Engine) chatScriptsPrompt() string {
	return "\nWhen the answer needs a calculation or a selection over the user's data " +
		"(sums per period, filters, counts), do not guess: write a JavaScript block and it will be " +
		"executed on the real data, then you will get the result and answer.\n" +
		"Format (title in Russian, plain JavaScript, return a value):\n" +
		"```sema:Название скрипта\nconst list = sema.receipts({ since: sema.date('начало месяца') });\n" +
		"return list.length;\n```\n" +
		"All sema.* calls are synchronous: never use async, await or Promises — " +
		"code after an await will not run.\n" +
		"Tools available right now:\n" + e.ToolsHint() +
		"When the user states a lasting rule or preference (what not to touch, how to report, thresholds), " +
		"propose to remember it: a ```sema-rule:<module name or sema> block with the rule text in Russian; " +
		"it is saved only after the user confirms.\n" +
		"If the user asks for a lasting ability (a new section, a recurring job, an integration), " +
		"propose a module instead of a script: a JSON manifest, a line with three dashes, then the code, " +
		"inside a ```sema-module block. Manifest fields: name, title, about, needs (any of " +
		"mail, files, http, search, mcp, store, llm, chat, schedule, ui, telegram, max, image), secrets (names of vault secrets the module needs, " +
		"e.g. [\"gitlab-token\"]; read them in code with sema.secret(name) — granted per name by the user), " +
		"memory (names of memory areas the module needs; in code sema.memory.recall(query) / sema.memory.remember(area, text)), " +
		"mcp (names of MCP servers the module needs, with needs mcp; in code sema.mcp.tools(server) lists tools and sema.mcp.call(server, tool, args) returns the result — " +
		"a changing call returns {pending, why} and waits for the user's button, so read r.output or the result object, never String(r)), " +
		"ssh (server names the module needs; in code sema.ssh.run(host, cmd) returns {output, code} for read-only commands and {pending, why} for commands that wait for the user — read r.output, never String(r)), " +
		"skills (names of Sema's skills the module relies on; in code sema.skill(name) returns the skill text to prepend to sema.llm prompts — analyse server output by the skill's rules, not from memory), " +
		"on ([\"message\", \"attachment\"] — functions onMessage(m)/onAttachment(f) run on every new mail/file with text; m has from, subject, date, text, files, local), " +
		"context (function name: context({query}) returns a short string the module knows for the chat), " +
		"blocks ({\"inbox\": \"section\"} — function returns components to show on the Mail screen), " +
		"own data via sema.data.put/get/find/sum/delete(collection, …) — JSON documents private to the module (no right needed), " +
		"tasks, screens. Ask for the fewest rights. " +
		// 4 октября 2026 модуль «Дневник» завёл функцию settingsSchema(), которую
		// никто не показывает: как сделать экран, модель не знала
		"A module whose user must set something (an address, people, how often) gets a settings screen — never ask for such values in the chat: " +
		"manifest \"screens\": [{\"id\": \"settings\", \"title\": \"Настройки\", \"build\": \"settingsScreen\"}] with needs ui; " +
		"settingsScreen() returns the screen as JSON: {heading: '…', components: [{type: 'form', title: '…', " +
		"fields: [{type: 'text'|'toggle'|'picker', name, label, value, hint, options}], " +
		"submit: {action: 'module_action', target: '<module name>~saveSettings', label: 'Сохранить'}}]}; " +
		"saveSettings(p) gets the fields by name as strings and keeps them with sema.data.put('settings', 'main', {…}); " +
		"read them back with sema.data.get('settings', 'main'). Passwords are not fields: they are secrets. " +
		"A site login that keeps a cookie session (login page → form → data) needs sema.http.session(): plain sema.http.get/post keep no cookies " +
		"and return only the body; a password sent as a hash — sema.crypto.md5/sha1/sha256(text, {encoding: 'windows-1251'}). " +
		"A recurring server check (updates, intrusions, VPN state) is a module with needs ssh, llm, chat, schedule, store; " +
		"ssh listing the servers; skills [\"linux-admin\", \"mikrotik\"]; a daily task; " +
		"the code runs read-only commands, keeps a short summary per server in sema.data to compare with yesterday, " +
		"asks sema.llm with the skill text plus the summary, and posts one line per server to a chat via sema.chat.\n" +
		"When the user asks to be reminded (напомни…), never answer in words and never say the date is invalid: " +
		"write a script that calls the reminder tool from the list, for example\n" +
		"```sema:Напоминание\nreturn sema.attention.remind('оплатить сервер', 'в понедельник', 85);\n```\n" +
		"when is ANY Russian date phrase — a weekday («в понедельник», «в пятницу»), «завтра», «через 3 дня», «10 сентября»; " +
		"source is the message id if the user named one, else ''.\n" +
		"sema.date('начало месяца'|'вчера'|'7 дней назад'|'в пятницу'|'через 3 дня'|'10 сентября') — человеческие даты; " +
		"omit since to take everything ever stored. Write the block only when it is really needed.\n" +
		"Links in the user's message are already read by Sema itself before you answer: their text is in the context below, " +
		"so never write a script that downloads them. To read some OTHER page from a script or module use sema.web.page(url) — " +
		"clean text, title and links; sema.http.get is for APIs and raw responses, never for reading pages.\n" +
		e.webFindPromptHint()
}

// installedModulesPrompt — какие модули уже стоят, чтобы модель дописывала
// их, а не собирала новые. Код показывается только у модуля, о котором
// идёт речь: слова вопроса сравниваются с его именем и названием.
func (e *Engine) installedModulesPrompt(userText string) string {
	mods := e.Modules()
	if len(mods) == 0 {
		return ""
	}
	mentioned := func(m *Module) bool { return moduleMentioned(m, userText) }
	var b strings.Builder
	b.WriteString("\nInstalled modules. To CHANGE one: either a ```sema-module block with the SAME name and the FULL new code, " +
		"or — for a big module — a ```sema-module-patch:<name> block with ONLY the changed or new top-level declarations " +
		"(whole `function name(...) {...}` and whole `var X = ...` statements; same-named ones are replaced, new ones appended), " +
		"optionally preceded by a JSON object with only the manifest fields that change (e.g. {\"tasks\": [...]}) and \"remove\": [\"fnName\"] to delete declarations. " +
		"Never wrap code in (function(){...})(): functions must be top-level. Sema keeps the module's rights and data):\n")
	for _, m := range mods {
		state := "выключен"
		if m.Enabled {
			state = "включён"
		}
		kind := ""
		if m.Builtin {
			kind = " · встроенный: изменения пишите под новым name, это будет копия"
		}
		fmt.Fprintf(&b, "- %s («%s», %s%s): %s\n", m.Name, m.Title, state, kind, clip(m.About, 200))
		if mentioned(m) && len(mods) > 0 {
			raw, _ := json.Marshal(map[string]any{"name": m.Name, "title": m.Title, "about": m.About, "needs": m.Needs,
				"tools": m.Tools, "tasks": m.Tasks, "screens": m.Screens, "on": m.On, "context": m.Context, "blocks": m.Blocks,
				"secrets": m.Secrets, "memory": m.Memory, "ssh": m.SSH})
			fmt.Fprintf(&b, "  current manifest: %s\n", raw)
			b.WriteString(e.moduleBody(m, userText))
		}
	}
	return b.String()
}

// proposeModule запоминает предложение Сёмы: новый модуль ставится
// выключенным и ждёт прав на экране «Модули»; существующий — обновляется
// целиком, права и данные остаются. Встроенный модуль из чата не правится:
// предложение становится копией под новым именем.
func (e *Engine) proposeModule(chatID int64, p *ProposedModule) {
	old, exists := e.Module(p.Module.Name)
	if exists && old.Builtin {
		p.Module.Name = old.Name + "-my"
		if p.Module.Title == old.Title {
			p.Module.Title = old.Title + " (мой)"
		}
		old, exists = e.Module(p.Module.Name)
	}
	if err := e.InstallModule(p.Module); err != nil {
		_ = e.store.AddChatMsg(e.chatForOutput(chatID), "assistant", "Не получилось сохранить модуль: "+err.Error())
		return
	}
	card := ToolCard{
		Title: "Модуль «" + p.Module.Title + "»",
		Code:  p.Module.Code,
	}
	switch {
	case exists && old.Enabled:
		if m, ok := e.Module(p.Module.Name); ok {
			_ = e.syncModuleTasks(m)
		}
		card.Result = "обновлён и работает · права: " + strings.Join(p.Module.Needs, ", ") + needsDiff(old.Needs, p.Module.Needs)
	case exists:
		card.Result = "обновлён, остаётся выключенным · включите на экране «Модули»"
	default:
		card.Result = "установлен выключенным · права: " + strings.Join(p.Module.Needs, ", ") + " · включите на экране «Модули»"
	}
	_ = e.saveToolCard(chatID, card)
}

// needsDiff — какие права модуль запросил сверх прежних: их видно в карточке.
func needsDiff(old, now []string) string {
	had := map[string]bool{}
	for _, n := range old {
		had[n] = true
	}
	var added []string
	for _, n := range now {
		if !had[n] {
			added = append(added, n)
		}
	}
	if len(added) == 0 {
		return ""
	}
	return " · новые права: " + strings.Join(added, ", ")
}

// ParseModuleFromAnswer — разбор ответа модели целиком (для диагностики).
func ParseModuleFromAnswer(answer string) (*Module, error) {
	block, _, ok := extractModuleBlock(answer)
	if !ok {
		return nil, fmt.Errorf("в ответе нет блока sema-module")
	}
	return ParseModule(block)
}

// moduleBody — код модуля для промпта. Большой модуль идёт оглавлением:
// девятьсот строк ради одной правки — это выброшенные токены. Полный
// текст даётся у функции, о которой спрашивают, и целиком — если модуль
// невелик.
func (e *Engine) moduleBody(m *Module, userText string) string {
	limit := e.ModuleCodeLimit()
	if strings.Count(m.Code, "\n")+1 <= limit {
		return "  current code:\n" + m.Code + "\n"
	}
	code, err := ParseModuleCode(m.Code)
	if err != nil || len(code.Funcs) == 0 {
		return "  current code:\n" + clip(m.Code, 12000) + "\n"
	}
	low := strings.ToLower(userText)
	var b strings.Builder
	fmt.Fprintf(&b, "  code outline (%d lines total; ask about a function to see it in full):\n%s",
		strings.Count(m.Code, "\n")+1, code.Outline())
	for _, f := range code.Funcs {
		if len(f.Name) >= 3 && strings.Contains(low, strings.ToLower(f.Name)) {
			fmt.Fprintf(&b, "  function %s in full:\n%s\n", f.Name, f.Body)
		}
	}
	b.WriteString("  This module is big: change it with a ```sema-module-patch:" + m.Name + " block carrying only the changed top-level functions and variables in full; ask to see a function before rewriting it.\n")
	return b.String()
}

// ModuleCodeLimit — до скольких строк модуль идёт в промпт целиком.
func (e *Engine) ModuleCodeLimit() int { return e.settingInt("modules.code_lines", 200) }

// moduleMentioned — идёт ли речь об этом модуле. Сравниваются первые четыре
// буквы слов: «курс» ловит и «курсы», и «курса». Той же приметой
// пользуется разбор закрытого вопроса, чтобы связать урок с модулем.
func moduleMentioned(m *Module, text string) bool {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 'а' && r <= 'я' || r == 'ё')
	}) {
		if r := []rune(w); len(r) >= 4 {
			words[string(r[:4])] = true
		}
	}
	for _, w := range strings.Fields(strings.ToLower(m.Name + " " + m.Title + " " + m.About)) {
		if r := []rune(strings.Trim(w, ",.«»()")); len(r) >= 4 && words[string(r[:4])] {
			return true
		}
	}
	return false
}

// ModuleOfChat — о каком модуле шёл разговор. Пусто — ни о каком: связывать
// урок наугад хуже, чем не связывать.
func (e *Engine) ModuleOfChat(chatID int64) string {
	msgs, err := e.store.ChatMsgs(chatID, 60)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == "user" || m.Role == "assistant" {
			b.WriteString(clip(m.Content, 400))
			b.WriteString("\n")
		}
	}
	text := b.String()
	var found string
	for _, m := range e.Modules() {
		if !moduleMentioned(m, text) {
			continue
		}
		if found != "" {
			return "" // речь о двух сразу — связывать не с чем
		}
		found = m.Name
	}
	return found
}

package engine

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/netguard"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Скрипты — то, чем Сёма расширяет себя сама. JavaScript выполняется внутри
// сервера; доступ к почте, файлам, модели и сети даётся явными функциями.

// scriptAPI собирает набор возможностей для одного запуска.
// builtinTools — встроенные инструменты песочницы. Раньше это была жёсткая
// структура на десять полей; теперь такой же реестр, какой пополняют модули,
// поэтому новая возможность больше не требует правки движка скриптов.
func (e *Engine) builtinTools() []script.Tool { return e.builtinToolsAs("") }

// builtinToolsAs — те же инструменты, но вызовы модели подписаны потребителем
// (модуль, скрипт, чат): песочница контекста не передаёт, поэтому подпись
// зашивается в замыкания на время запуска.
func (e *Engine) builtinToolsAs(consumer string) []script.Tool {
	return e.builtinToolsFor(consumer, nil)
}

// builtinToolsFor — те же инструменты, но с подстановкой секретов в
// заголовки: её умеет только модуль, которому имя секрета выдано.
func (e *Engine) builtinToolsFor(consumer string, fill secretFiller) []script.Tool {
	bg := func() context.Context { return llm.WithConsumer(context.Background(), consumer) }
	tools := []script.Tool{
		{
			Name: "messages", Need: "mail", Args: "{since, status, from, subject, account, limit}",
			Desc: "письма из архива: from, subject, date, status, snippet. " +
				"По умолчанию берутся только разобранные не-спам (status inbox_ok), " +
				"поэтому за сутки часто выходит пусто: свежее лежит под server_spam, pending, archived. " +
				"status: \"any\" — все подряд. since — с какого времени, например sema.date(\"вчера\")",
			Fn: func(a []any) (any, error) { return e.toolMessages(script.Opts(a, 0)) },
		},
		{
			Name: "files", Need: "files", Args: "{kind}",
			Desc: "вложения из писем: filename, kind, vendor, amount, text",
			Fn:   func(a []any) (any, error) { return e.toolFiles(script.Opts(a, 0)) },
		},
		{
			Name: "files.add", Need: "files", Args: "(name, content, {mime, base64})",
			Desc: "положить файл в хранилище Сёмы: текст как есть или base64 для двоичных; источник — модуль; возвращает id",
			Fn:   func(a []any) (any, error) { return e.toolAddFile(bg(), consumer, a) },
		},
		{
			Name: "files.text", Need: "files", Args: "(id)",
			Desc: "извлечённый текст файла по id",
			Fn: func(a []any) (any, error) {
				att, err := e.store.AttachmentByID(int64(numArg(a, 0)))
				if err != nil {
					return nil, fmt.Errorf("файл не найден")
				}
				return att.Extracted, nil
			},
		},
		{
			Name: "files.transcribe", Need: "files", Args: "(id)",
			Desc: "расшифровать аудиозапись (голосовое, диктофон) по id файла и вернуть текст; вызывай только когда владелец " +
				"сам попросил расшифровать запись: записи из почты и папок Сёма сам не расшифровывает, а каждая расшифровка стоит денег. " +
				"Работает, когда на роль «Распознавание речи» назначена модель; иначе — ошибка с причиной. Уже расшифрованное возвращается без новой платы",
			Fn: func(a []any) (any, error) { return e.toolTranscribe(bg(), consumer, int64(numArg(a, 0))) },
		},
		{
			Name: "files.mark", Need: "files", Args: "(id, {kind, vendor, amount, currency, summary})",
			Desc: "пометить файл тем, что модуль в нём понял: вид (receipt|document), получатель, сумма",
			Fn:   func(a []any) (any, error) { return nil, e.toolMarkFile(a) },
		},
		{
			Name: "ask", Need: "llm", Args: "(prompt)",
			Desc: "спросить модель с контекстом из писем",
			Fn:   func(a []any) (any, error) { return e.toolAsk(bg(), script.Str(a, 0)) },
		},
		{
			Name: "llm", Need: "llm", Args: "(prompt, {local})",
			Desc: "ответ модели без контекста; trusted: true — только доверенная модель (для закрытых ящиков; local — то же, старое имя)",
			Fn:   func(a []any) (any, error) { return e.toolLLM(bg(), script.Str(a, 0), script.Opts(a, 1)) },
		},
		{
			Name: "chat", Need: "chat", Args: "(chat, text)",
			Desc: "написать в чат",
			Fn:   func(a []any) (any, error) { return nil, e.toolChat(consumer, script.Str(a, 0), script.Str(a, 1)) },
		},
		{
			Name: "telegram.post", Need: "telegram", Args: "(channel, text, {image})",
			Desc: "опубликовать пост от имени владельца в его Telegram-канал по названию канала. " +
				"Только каналы, где в «Настройки → Telegram» включены «Писать» и «Публиковать в канал»; " +
				"не больше tg.posts_per_day постов в сутки (по умолчанию 3). " +
				"image — номер файла картинки из sema.image. Пост до 1024 знаков уходит фото с подписью; длиннее (до 4096) — " +
				"текстом, над которым картинка стоит крупным превью. Без картинки — до 4096. " +
				"Возвращает {channel, today, limit}; отказ — ошибкой с причиной",
			Fn: func(a []any) (any, error) { return e.toolTGPost(bg(), a) },
		},
		{
			Name: "max.post", Need: "max", Args: "(channel, text, {image})",
			Desc: "опубликовать пост от имени владельца в его канал MAX по названию канала — тем же текстом, что в Telegram. " +
				"Только каналы, где в «Настройки → MAX» включено «Писать», и при включённой отправке от вашего имени; " +
				"не больше max.posts_per_day постов в сутки (по умолчанию 3). Текст до 4000 знаков. " +
				"image — номер файла картинки из sema.image: уходит фотографией вместе с текстом одним сообщением. " +
				"Возвращает {channel, today, limit, how}; отказ — ошибкой с причиной",
			Fn: func(a []any) (any, error) { return e.toolMAXPost(bg(), a) },
		},
		{
			Name: "image", Need: "image", Args: "(prompt)",
			Desc: "нарисовать картинку по описанию через fal.ai самой дешёвой моделью (подбирается по ценам fal раз в сутки), " +
				"размер 1280×720. Описание пиши по-английски и без личных данных: оно уходит из дома, секретное не отправляется. " +
				"Каждая картинка стоит денег. Возвращает {file, model, price, currency}; file — номер для sema.telegram.post {image}",
			Fn: func(a []any) (any, error) { return e.toolImage(bg(), a) },
		},
		{
			Name: "search", Need: "search", Args: "(query)",
			Desc: "поиск в интернете ролью «Поиск»: {answer, links, sources}; links — адреса строками, sources — [{url, title, date}] с заголовком и датой публикации, если поисковик их назвал; личное из запроса вырезается",
			Fn: func(a []any) (any, error) {
				res := e.Search(bg(), script.Str(a, 0))
				if res.Err != "" {
					return nil, fmt.Errorf("%s", res.Err)
				}
				sources := make([]any, 0, len(res.Sources))
				for _, src := range res.Sources {
					sources = append(sources, map[string]any{"url": src.URL, "title": src.Title, "date": src.Date})
				}
				return map[string]any{"answer": res.Answer, "links": res.Links, "sources": sources, "query": res.Query}, nil
			},
		},
		{
			Name: "web.page", Need: "http", Args: "(url, {save, chars, links})",
			Desc: "прочитать страницу по адресу: ядро само скачивает, выбирает основной текст (без меню и подвала) и отдаёт " +
				"{url, title, date, text, size, truncated, data, dataTotal, feeds, links, linksTotal, file}. " +
				"text — текст статьи, первые chars знаков (из коробки " + strconv.Itoa(pageCharsDefault) + ", человек может изменить в настройках; size — сколько знаков всего, truncated — обрезан ли). " +
				"date — когда опубликована, ISO 8601, из разметки страницы; пусто, если разметка молчит (дату с экрана ядро не выдумывает). " +
				"data — объекты JSON-LD со страницы как есть, до " + strconv.Itoa(pageDataMax) + " штук (dataTotal — сколько их было): " +
				"у карточки товара это Product.offers.price, у статьи NewsArticle с автором и датой. " +
				"feeds — адреса лент RSS/Atom страницы: если текста нет, а лента есть, читай её через sema.http.get. " +
				"links — [{url, text}] с абсолютными адресами со всей страницы, включая меню, первые links штук (из коробки " + strconv.Itoa(pageLinksDefault) + "; linksTotal — сколько всего): " +
				"по ним идут от страницы-списка к страницам-статьям. " +
				"Страница, что собирается скриптом в браузере (пустой <body>, ни JSON-LD, ни лент), — ошибка «на странице N знаков текста при пределе M (настройка web.page_min_chars) — похоже, она собирается скриптом в браузере»: короче web.page_min_chars знаков (из коробки " + strconv.Itoa(pageMinCharsDefault) + ") считается пустой. " +
				"Каждый вызов ходит в сеть за свежей копией и ничего не сохраняет; {save: true} кладёт текст ещё и в «Файлы» (нужно право files), file — его номер, 0 если не сохраняли. " +
				"Читать страницы надо этим, а не срезом от sema.http.get: у страницы в сотни килобайт текст начинается после шапки и скриптов. " +
				"Сырой HTML, JSON и API — sema.http.get",
			Fn: func(a []any) (any, error) { return e.toolWebPage(bg(), consumer, a) },
		},
		{
			Name: "http.get", Need: "http", Args: "(url, {headers})",
			Desc: "запрос в сеть, ответ — сырая строка (HTML, JSON): для API и сырых ответов; " +
				"чтобы прочитать страницу и получить текст и ссылки — sema.web.page. " +
				"В заголовке можно писать {{secret:имя}} — значение подставит ядро, токен в текст модели не попадёт",
			Fn: func(a []any) (any, error) {
				h, err := fillHeaders(headersOpt(script.Opts(a, 1)), fill)
				if err != nil {
					return nil, err
				}
				return httpRequest("GET", script.Str(a, 0), "", "", h)
			},
		},
		{
			Name: "http.post", Need: "http", Args: "(url, body, {headers, contentType})",
			Desc: "отправить данные; третьим аргументом можно передать строку типа содержимого или объект {headers, contentType}",
			Fn: func(a []any) (any, error) {
				ct := script.Str(a, 2)
				opts := script.Opts(a, 2)
				if v, ok := opts["contentType"].(string); ok {
					ct = v
				}
				h, err := fillHeaders(headersOpt(opts), fill)
				if err != nil {
					return nil, err
				}
				return httpRequest("POST", script.Str(a, 0), script.Str(a, 1), ct, h)
			},
		},
		{
			Name: "http.request", Need: "http", Args: "(method, url, body, {headers, contentType})",
			Desc: "запрос любым методом: PUT, PATCH, DELETE",
			Fn: func(a []any) (any, error) {
				opts := script.Opts(a, 3)
				ct, _ := opts["contentType"].(string)
				h, err := fillHeaders(headersOpt(opts), fill)
				if err != nil {
					return nil, err
				}
				return httpRequest(script.Str(a, 0), script.Str(a, 1), script.Str(a, 2), ct, h)
			},
		},
		{
			Name: "store.get", Need: "store", Args: "(key)",
			Desc: "память между запусками",
			Fn:   func(a []any) (any, error) { return e.store.ScriptMemory(script.Str(a, 0)), nil },
		},
		{
			Name: "store.set", Need: "store", Args: "(key, value)",
			Desc: "запомнить между запусками",
			Fn: func(a []any) (any, error) {
				return nil, e.store.SetScriptMemory(script.Str(a, 0), script.Str(a, 1))
			},
		},
		{
			Name: "task", Need: "schedule", Args: "(title, what, when)",
			Desc: "завести задачу по расписанию",
			Fn: func(a []any) (any, error) {
				return nil, e.toolTask(script.Str(a, 0), script.Str(a, 1), script.Str(a, 2))
			},
		},
		// в скриптах человека и чата ни секретов, ни памяти — заглушки объясняют, почему
		chatSecretStub(),
	}
	// поиск страниц лежит в песочнице всегда, а не только при заданном адресе
	// SearXNG: модуль, что уже стоит и зовёт sema.web.find, после очистки
	// адреса должен получить ошибку инструмента («не настроен»), а не
	// «is not a function» — то есть не сломанный код, от которого модуль
	// выключается. Про инструмент модели не говорится, пока адреса нет
	// (`ToolsHint`): новый код под него она писать не должна
	tools = append(tools, e.webFindTool(consumer))
	// сессия с cookie и хеши — вход на сайт, который держит сессию cookie
	// и шлёт пароль хешем (module-http-session)
	tools = append(tools, httpSessionTool(fill))
	tools = append(tools, cryptoTools()...)
	// ссылка на файл для чужой системы (file-share-links)
	tools = append(tools, e.shareTool(consumer))
	return tools
}

// builtinToolsWithStubs — встроенные плюс заглушки памяти (для скриптов).
func (e *Engine) builtinToolsWithStubs(consumer string) []script.Tool {
	// уведомления и напоминания без права — и скриптам чата тоже: подсказка
	// обещала sema.remind, а песочница его не клала, и модель получала
	// «Cannot read property 'remind' of undefined»
	tools := append(append(e.builtinToolsAs(consumer), memoryStubTools()...), sshStubTools()...)
	return append(tools, e.notifyTools(consumer)...)
}

// ToolsFor — инструменты, доступные при перечисленных правах. Пустой список
// прав — только то, что права не требует (уведомления): скрипты самого
// человека идут другим путём, builtinToolsWithStubs.
// Заглушка секретов сюда не попадает: у модуля вместо неё настоящий примитив.
func (e *Engine) ToolsFor(needs []string) []script.Tool {
	return e.toolsFor(needs, "", "")
}

// toolsFor — то же, но с именем модуля-вызывающего: свои инструменты через
// песочницу он не получает.
func (e *Engine) toolsFor(needs []string, except, consumer string) []script.Tool {
	return e.toolsForFill(needs, except, consumer, nil)
}

func (e *Engine) toolsForFill(needs []string, except, consumer string, fill secretFiller) []script.Tool {
	all := append(e.builtinToolsFor(consumer, fill), e.moduleTools(except)...)
	all = append(all, e.notifyTools(consumer)...) // уведомления и напоминания — без права
	// пустой список прав — не «всё», а «ничего, кроме того, что права не
	// требует»: модуль без needs получал почту и сеть целиком
	allowed := map[string]bool{"": true}
	for _, n := range needs {
		allowed[n] = true
	}
	var out []script.Tool
	for _, t := range all {
		if allowed[t.Need] {
			out = append(out, t)
		}
	}
	return out
}

// ToolsHint — описание инструментов для модели, собранное из реестра.
// Раньше этот список я писал в промпте руками и он расходился с кодом.
func (e *Engine) ToolsHint() string {
	var b strings.Builder
	for _, t := range append(e.builtinTools(), e.moduleTools("")...) {
		if t.Name == webFindToolName && !e.WebFindReady() {
			continue // про поиск страниц молчим, пока не задан адрес SearXNG
		}
		fmt.Fprintf(&b, "sema.%s%s — %s\n", t.Name, t.Args, t.Desc)
	}
	return b.String()
}

func (e *Engine) RunScript(ctx context.Context, code string) script.Result {
	consumer := llm.ConsumerOf(ctx)
	if consumer == "" {
		consumer = "script"
	}
	tools := append(e.builtinToolsWithStubs(consumer), e.moduleTools("")...)
	tools = append(tools, e.chatMCPTools(consumer)...) // серверы MCP: скрипту чата — открытые чату, остальным — объяснение
	started := time.Now()
	res := script.Run(ctx, code, tools, e.ScriptTimeout())
	e.recordRun(consumer, time.Since(started).Seconds(), res.Err == nil, res.ErrText)
	return res
}

// ScriptTimeout — предел времени скрипта из чата и сохранённого скрипта
// (scripts.timeout_s). Было 30 секунд в коде: превью статьи — поиск,
// текст моделью и картинка — не укладывалось ни разу, и 17 сентября 2026
// Сёма трижды упёрся в предел. Время здесь не критично, поэтому по
// умолчанию пять минут, а зависший цикл всё равно прервётся.
func (e *Engine) ScriptTimeout() time.Duration {
	var v int
	if _, err := fmt.Sscanf(e.store.Setting("scripts.timeout_s", ""), "%d", &v); err == nil && v >= 10 {
		return time.Duration(v) * time.Second
	}
	return 5 * time.Minute
}

// RunScriptByID — запуск сохранённого скрипта (в том числе из задачи).
func (e *Engine) RunScriptByID(ctx context.Context, id int64) (script.Result, error) {
	s, err := e.store.Script(id)
	if err != nil {
		return script.Result{}, err
	}
	res := e.RunScript(llm.WithConsumer(ctx, "script:"+strconv.FormatInt(id, 10)), s.Code)
	out := res.Value
	if res.Err != nil {
		out = "ошибка: " + res.Err.Error()
	}
	_ = e.store.ScriptRan(id, out, res.Duration.Seconds())
	return res, nil
}

var scriptHTTP = netguard.Client(20 * time.Second)

func intOpt(opts map[string]any, key string, def int) int {
	switch v := opts[key].(type) {
	case int64:
		return int(v)
	case float64:
		return int(v)
	case int:
		return v
	default:
		return def
	}
}

func timeOpt(opts map[string]any, key string) time.Time {
	s, _ := opts[key].(string)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// Ниже — сами инструменты. Каждый отдаёт скрипту простые значения, а не
// внутренние типы Сёмы: так модуль и скрипт остаются независимыми от кода.

func (e *Engine) toolMessages(opts map[string]any) (any, error) {
	status, _ := opts["status"].(string)
	if status == "" {
		status = store.StatusInboxOK
	}
	limit := intOpt(opts, "limit", 100)
	var msgs []*store.Message
	var err error
	// since/from/subject/account — выборка за период с фильтрами, а не только по статусу
	since := timeOpt(opts, "since")
	from, _ := opts["from"].(string)
	subject, _ := opts["subject"].(string)
	account, _ := opts["account"].(string)
	if !since.IsZero() || from != "" || subject != "" || account != "" {
		if since.IsZero() {
			since = time.Now().AddDate(0, 0, -90)
		}
		statuses := []string{status}
		if status == "any" {
			statuses = nil
		}
		msgs, err = e.store.MessagesBetween(account, nil, since, time.Now().Add(time.Hour), statuses...)
		if err == nil {
			kept := msgs[:0:0]
			for _, m := range msgs {
				if from != "" && !strings.Contains(strings.ToLower(m.FromAddr), strings.ToLower(from)) {
					continue
				}
				if subject != "" && !strings.Contains(strings.ToLower(m.Subject), strings.ToLower(subject)) {
					continue
				}
				kept = append(kept, m)
				if len(kept) >= limit {
					break
				}
			}
			msgs = kept
		}
	} else {
		msgs, err = e.store.ByStatus(status, nil, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{
			"id": m.ID, "from": m.FromAddr, "name": m.FromName, "subject": m.Subject,
			"date": m.Date.Format(time.RFC3339), "status": m.Status,
			"snippet": m.Snippet, "account": m.Account,
		})
	}
	return out, nil
}

// toolAddFile — файл от модуля: тот же путь, что вложение и загрузка.
func (e *Engine) toolAddFile(ctx context.Context, consumer string, a []any) (any, error) {
	name := strings.TrimSpace(script.Str(a, 0))
	if name == "" {
		return nil, fmt.Errorf("files.add: нужно имя файла")
	}
	content := script.Str(a, 1)
	o := script.Opts(a, 2)
	data := []byte(content)
	if b64, _ := o["base64"].(bool); b64 {
		raw, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("files.add: base64: %w", err)
		}
		data = raw
	}
	mime, _ := o["mime"].(string)
	ref := strings.TrimPrefix(consumer, "module:")
	kind := store.SourceModule
	if !strings.HasPrefix(consumer, "module:") {
		kind, ref = store.SourceUpload, consumer
	}
	att, _, err := e.AddFile(ctx, name, mime, data, FileSource{Kind: kind, Ref: ref}, nil)
	if err != nil {
		return nil, err
	}
	return att.ID, nil
}

func numArg(a []any, i int) float64 {
	if i >= len(a) {
		return 0
	}
	switch v := a[i].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// toolMarkFile — модуль помечает файл тем, что понял: «Файлы» покажут вид и
// сумму, поиск по виду файла заработает. Текст файла не трогаем.
func (e *Engine) toolMarkFile(a []any) error {
	var id int64
	switch v := script.Str(a, 0); {
	case v != "":
		fmt.Sscanf(v, "%d", &id)
	default:
		if f, ok := a[0].(float64); ok {
			id = int64(f)
		} else if i, ok := a[0].(int64); ok {
			id = i
		}
	}
	if id == 0 {
		return fmt.Errorf("files.mark: нужен id файла")
	}
	o := script.Opts(a, 1)
	kind, _ := o["kind"].(string)
	vendor, _ := o["vendor"].(string)
	currency, _ := o["currency"].(string)
	summary, _ := o["summary"].(string)
	amount := 0.0
	switch v := o["amount"].(type) {
	case float64:
		amount = v
	case int64:
		amount = float64(v)
	}
	return e.store.MarkAttachment(id, kind, vendor, amount, currency, summary)
}

func (e *Engine) toolFiles(opts map[string]any) (any, error) {
	kind, _ := opts["kind"].(string)
	atts, err := e.store.Attachments(kind, intOpt(opts, "limit", 200))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(atts))
	for _, a := range atts {
		out = append(out, map[string]any{
			"id": a.ID, "filename": a.Filename, "kind": a.Kind, "vendor": a.Vendor,
			"amount": a.Amount, "currency": a.Currency, "size": a.Size,
			"summary": a.Summary, "text": clip(a.Extracted, 4000),
			"from": a.FromAddr, "date": a.Date.Format(time.RFC3339),
		})
	}
	return out, nil
}

func (e *Engine) toolAsk(ctx context.Context, prompt string) (any, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", nil
	}
	return e.Ask(ctx, prompt)
}

// toolLLM — ответ модели без RAG: модуль сам собрал, что спросить. local —
// только локальная модель: так письма закрытого ящика не уходят наружу;
// нет локальной — честный отказ, а не тихий уход в облако.
func (e *Engine) toolLLM(ctx context.Context, prompt string, opts map[string]any) (any, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", nil
	}
	// trusted — по смыслу: доверенная модель, а не «на этой машине»; local
	// остаётся синонимом, иначе встроенный модуль чеков сломался бы
	local, _ := opts["local"].(bool)
	if t, ok := opts["trusted"].(bool); ok && t {
		local = true
	}
	if err := e.budgetCheck(ctx); err != nil {
		return nil, err
	}
	if local {
		model := e.localModelOrNone()
		if model == roleNoneName {
			return nil, fmt.Errorf("доверенной модели нет: письмо из закрытого ящика не разбираем — назначьте доверенную модель на «Моделях»")
		}
		p := e.provider(model)
		if p == nil {
			return nil, fmt.Errorf("модель %q не подключена", model)
		}
		ctx, cancel := context.WithTimeout(ctx, e.modelToolTimeout(model))
		defer cancel()
		// наружу не уходит: модель здесь уже выбрана по закрытости ящика —
		// для закрытого берётся домашняя, а её нет — выше стоит отказ
		return p.Complete(ctx, prompt)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// модуль сам собрал текст: класс проверяется дверью, закрытое сюда не доходит (trusted выше)
	out, _, err := e.askRoleGuarded(ctx, RoleModules, prompt, true)
	return out, err
}

// modelToolTimeout — сколько ждать ответа sema.llm.
//
// Облачной модели хватает двух минут. Модели на этой машине — нет: qwen3 на
// Raspberry Pi разбирает промпт в две тысячи токенов дольше, и модуль
// «Серверы под присмотром» четыре ночи подряд упирался в те же две минуты,
// ни разу не дождавшись сводки (10–13 сентября 2026). Домашней модели
// даётся столько, сколько задаче модуля целиком — предел, который человек
// уже выставил на «Модулях».
func (e *Engine) modelToolTimeout(model string) time.Duration {
	if e.ModelOnThisMachine(model) {
		return time.Duration(e.settingInt(ModuleTaskTimeoutKey, 600)) * time.Second
	}
	return 2 * time.Minute
}

// toolChat пишет в чат по названию, заводя его при необходимости: скрипту
// удобнее говорить «Отчёты», чем помнить номер.
func (e *Engine) toolChat(consumer, title, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if title == "" {
		title = "Сообщения от скриптов"
	}
	chats, err := e.store.Chats()
	if err != nil {
		return err
	}
	var id int64
	for _, c := range chats {
		if c.Title == title {
			id = c.ID
			break
		}
	}
	if id == 0 {
		if id, err = e.store.CreateChat(title); err != nil {
			return err
		}
	}
	// блоки sema-ssh:<сервер> в сообщении модуля — карточки, как у чата:
	// чтение выполняется, изменение ждёт кнопки; серверы — только выданные
	if name, ok := strings.CutPrefix(consumer, "module:"); ok {
		if m, found := e.Module(name); found {
			hosts := m.GrantedHosts
			if hosts == nil {
				hosts = []string{}
			}
			o := SSHOrigin{Who: "модуль «" + m.Title + "»", ChatID: id, Hosts: hosts}
			if err := e.store.AddChatMsg(e.chatForOutput(id), "assistant", text); err != nil {
				return err
			}
			e.runSSHBlocks(context.Background(), id, text, o)
			return nil
		}
	}
	return e.store.AddChatMsg(e.chatForOutput(id), "assistant", text)
}

func (e *Engine) toolTask(title, what, when string) error {
	if what == "" {
		what = title
	}
	if when == "" {
		when = "каждый час"
	}
	return e.CreateTask(what, when)
}

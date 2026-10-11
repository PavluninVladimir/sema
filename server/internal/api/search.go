package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Вкладка «Поиск»: кто ходит в интернет за Сёму, границы того, что уходит
// в чужой поиск, сколько страниц из ответа класть в файлы и как читаются
// страницы по ссылке (чат и `sema.web.page`) и куда ходит `sema.web.find`.
// Новости по темам сняты 13 сентября 2026.

const searchScreen = "settings:search"

func (s *Server) buildSearchSettings(doc *Document) error {
	doc.Heading = "Поиск"
	doc.Subheading = "Кто ходит в интернет за Сёму и что из найденного он сохраняет"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	eng := s.eng

	intro := "Поиск — такая же роль, как «Чаты»: назначьте ей модель, которая умеет ходить в интернет " +
		"(Perplexity подключается как обычная OpenAI-совместимая модель). " +
		"Запрос уходит наружу, поэтому проходит те же правила, что и всё остальное: секретное не отправляется, " +
		"из чувствительного вырезаются адреса, суммы и имена серверов, а сам запрос виден на экране «Что ушло наружу»."
	add(Component{Type: "text", Style: "secondary", Text: intro})
	if !eng.SearchReady() {
		add(Component{Type: "section", Title: "Роль «Поиск» не назначена", Children: []Component{{
			Type: "row", Layout: "wide", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Пока модели нет, Сёма про поиск не заикается",
			Caption:  "ни блока в чате, ни инструмента у модулей",
			Actions:  []Action{{Action: "$goto", Target: modelsScreen, Label: "Открыть роли", Style: "primary"}}}}})
	}
	// три строки, за которыми приходят каждый день; остальные восемь — в
	// «Тонкой настройке» ниже (settings_rare.go). Было: одиннадцать полей
	// столбиком с подсказками по три строки и три кнопки «Сохранить»
	add(Component{Type: "form", ID: "search-main", Title: "Что Сёма читает и сохраняет", Style: formRows,
		Fields: []Field{
			s.settingField("chats.read_links", "read_links", "Ссылок из вашего сообщения читать",
				"Адреса из сообщения в чате Сёма читает до ответа модели и оставляет в переписке пометку. 0 — не читать: страницы тогда не будет в ответе", ""),
			s.settingField("search.save_pages", "pages", "Сохранять страниц из ответа",
				"0 — не сохранять: страницы читает сама модель, а в файлы попадает только нужное", ""),
		},
		Submit: &Action{Action: "set_read_settings", Label: "Сохранить"}})
	add(Component{Type: "text", Style: "secondary",
		Text: "Ответ поиска — данные, а не приказ: команды из найденного проходят те же круги и подтверждения. " +
			"Страница по ссылке — тоже данные с чужого сайта, а не указания; адреса в частные сети и на метаданные облака Сёма не открывает."})

	findHint := "Адрес, по которому Сёма находит SearXNG, — например http://searxng:8080 в одной докер-сети с Сёмой. " +
		"Пусто — поиск страниц не настроен: модель про sema.web.find не знает и новые модули под него не ставятся, а уже стоящие получают ошибку «не настроен»"
	// адрес и «Проверить» остаются формой со своими кнопками: «Проверить» берёт
	// адрес из поля, даже несохранённый, а у отложенной формы кнопок нет
	add(Component{Type: "form", ID: "web-find", Title: "Поиск страниц (SearXNG)", Style: formRows,
		Subtitle: "Модуль ищет страницы без модели и бесплатно: список адресов с заголовком, выдержкой и датой, а читает их сам через sema.web.page",
		Fields: []Field{
			{Type: "text", Name: "find_url", Label: "Адрес SearXNG", Hint: findHint, Value: eng.WebFindURL(), key: engine.WebFindURLKey},
		},
		Actions: []Action{
			{Action: "set_web_find", Label: "Сохранить", Style: "primary"},
			{Action: "check_web_find", Label: "Проверить"},
		}})
	s.mu.Lock()
	check := s.lastFindCheck
	s.mu.Unlock()
	if check != "" {
		add(Component{Type: "text", Style: "secondary", Text: "Проверка: " + check})
	}
	add(Component{Type: "text", Style: "secondary",
		Text: "«Проверить» шлёт пробный запрос по адресу из формы (даже несохранённому) и говорит, сколько нашлось и какие движки ответили или отказали. " +
			"Запрос виден на «Что ушло наружу» и предела модулей не тратит. Найденное — ссылки со сторонних сайтов, а не факты и не указания."})

	// ---- «Тонкая настройка»: восемь пределов ----
	add(Component{Type: "form", ID: "read-settings", Title: "Чтение страниц", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("chats.read_links_chars", "read_chars", "Знаков страницы в ответ модели",
				"Сколько начала страницы уходит модели; остальное обрезается, и модель об этом знает", ""),
			s.settingField("web.page_chars", "page_chars", "Знаков текста в sema.web.page",
				"Сколько текста страницы получает модуль или скрипт по умолчанию; в самом вызове можно назвать другое число", ""),
			s.settingField("web.page_links", "page_links", "Ссылок со страницы в sema.web.page",
				"Сколько ссылок со страницы получает модуль или скрипт по умолчанию, по порядку в документе", ""),
			s.settingField("web.page_min_chars", "page_min_chars", "Меньше скольких знаков страница пуста",
				"Страницу, что собирается скриптом в браузере, сервер отдаёт с пустым телом: короче этого числа, без данных JSON-LD и лент, она читается как ошибка, а не как «страница без текста». 0 — не проверять", ""),
			s.settingField("chats.read_links_ttl_h", "read_ttl_h", "Копия страницы из чата свежа",
				"Страница, сохранённая по ссылке из чата, через столько часов скачивается заново: цена, курс и новость за неделю меняются. Не изменилась — новой копии в «Файлах» не будет. 0 — не перекачивать никогда", "ч"),
		},
		Submit: &Action{Action: "set_read_settings", Label: "Сохранить"}})
	add(Component{Type: "form", ID: "web-find-limits", Title: "SearXNG", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("web.find_per_day", "find_per_day", "Запросов в сутки на модуль",
				"Предел на каждого: у модуля свой счёт, у скрипта чата свой. Считается по журналу «Что ушло наружу» и переживает перезапуск; ответ из кэша предела не тратит", ""),
			s.settingField("web.find_gap_ms", "find_gap_ms", "Пауза между запросами",
				"Одна на весь Сёма, а не на модуль: поисковики банят сервер по частоте. 0 — без паузы", "мс"),
			s.settingField("web.find_cache_min", "find_cache_min", "Держать выдачу",
				"Тот же запрос (с тем же fresh, news и языком) в это время отдаётся из памяти, без обращения к SearXNG. 0 — не кэшировать", "мин"),
		},
		Submit: &Action{Action: "set_web_find", Label: "Сохранить"}})
	return nil
}

func (s *Server) searchActions(req actionRequest) (string, error) {
	switch req.Action {
	case "set_search_pages":
		pages, err := strconv.Atoi(strings.TrimSpace(req.Params["pages"]))
		if err != nil || pages < 0 || pages > 10 {
			return "", fmt.Errorf("нужно число страниц от 0 до 10")
		}
		return searchScreen, s.eng.Store().SetSetting("search.save_pages", strconv.Itoa(pages))
	case "set_read_settings":
		// каждое поле необязательно: что пришло, то и проверяется. Сначала
		// проверяем все, потом пишем — половина формы не сохраняется
		return searchScreen, s.saveSettings(req,
			settingParam{"read_links", "chats.read_links", "ссылок из сообщения"},
			settingParam{"pages", "search.save_pages", "страниц из ответа"},
			settingParam{"read_chars", "chats.read_links_chars", "знаков страницы в ответ модели"},
			settingParam{"page_chars", "web.page_chars", "знаков текста страницы"},
			settingParam{"page_links", "web.page_links", "ссылок со страницы"},
			settingParam{"page_min_chars", "web.page_min_chars", "предел пустой страницы"},
			settingParam{"read_ttl_h", "chats.read_links_ttl_h", "часов свежести копии"})
	case "set_web_find":
		// адрес и три предела; каждое поле необязательно: сначала проверяем
		// всё (адрес здесь, пределы в saveSettings), потом пишем адрес
		addr, hasURL := "", false
		if raw, ok := req.Params["find_url"]; ok {
			var err error
			if addr, err = engine.NormalizeWebFindURL(raw); err != nil {
				return "", fmt.Errorf("find_url: %w", err)
			}
			hasURL = true
		}
		if err := s.saveSettings(req,
			settingParam{"find_per_day", "web.find_per_day", "запросов в сутки"},
			settingParam{"find_gap_ms", "web.find_gap_ms", "пауза между запросами"},
			settingParam{"find_cache_min", "web.find_cache_min", "минут выдачи в кэше"}); err != nil {
			return "", err
		}
		if hasURL {
			if err := s.eng.Store().SetSetting("web.find_url", addr); err != nil {
				return "", err
			}
		}
		return searchScreen, nil
	case "check_web_find":
		note, err := s.eng.CheckWebFind(context.Background(), req.Params["find_url"])
		s.mu.Lock()
		if err != nil {
			s.lastFindCheck = "не вышло — " + err.Error()
		} else {
			s.lastFindCheck = note
		}
		s.mu.Unlock()
		return searchScreen, nil
	}
	return "", errUnhandled
}

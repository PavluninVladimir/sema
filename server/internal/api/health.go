package api

// Экран «Здоровье»: что с Сёмой и кто сколько съел. Данные — журнал расхода
// и снимки процесса; пороги жёлтого и красного — настройки на этом же экране.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/version"
)

const healthPeriodKey = "health.period"

var healthPeriods = []struct{ ID, Title string }{{"day", "сегодня"}, {"week", "неделя"}, {"month", "месяц"}}

func healthSince(period string) (time.Time, string) {
	now := time.Now()
	switch period {
	case "week":
		return now.Add(-7 * 24 * time.Hour), "неделя"
	case "month":
		return now.Add(-30 * 24 * time.Hour), "месяц"
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), "сегодня"
}

func levelColor(level string) string {
	switch level {
	case "bad":
		return "#C4271C"
	case "warn":
		return "#B7791F"
	}
	return "#1E7A32"
}

// valueUnit делит показатель на число и единицу: «72 °C» → «72»
// и «°C», «10 мин» → «10» и «мин». Не поделилось — значит это слово
// целиком («пусто», «1 мин назад»), и оно остаётся значением.
func valueUnit(v string) (string, string) {
	i := strings.IndexByte(v, ' ')
	if i <= 0 || i == len(v)-1 {
		return v, ""
	}
	for _, r := range v[:i] {
		if r < '0' || r > '9' {
			if r != ',' && r != '.' && r != '-' {
				return v, ""
			}
		}
	}
	хвост := strings.TrimSpace(v[i:])
	// хвост из нескольких слов — это уже пояснение, а не единица
	if strings.Count(хвост, " ") > 0 {
		return v, ""
	}
	// «1 249» — это одно число с пробелом между тысячами, а не число с
	// единицей «249»
	if digitsRow(хвост) {
		return v, ""
	}
	return v[:i], хвост
}

// digitsRow — в строке одни цифры.
func digitsRow(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cardLevel — слово состояния в уровень UI-DSL. «Норма молчит»:
// у спокойной плитки уровня нет вовсе, и полосы слева она не получает.
func cardLevel(level string) string {
	switch level {
	case "bad":
		return "alert"
	case "warn":
		return "warn"
	}
	return ""
}

func levelTitle(level string) string {
	switch level {
	case "bad":
		return "плохо"
	case "warn":
		return "внимание"
	}
	return "в норме"
}

// HealthDot — цвет точки у пункта меню.
// healthLevel — состояние Сёмы одним словом для точки в меню.
func (s *Server) healthLevel() string {
	_, level := s.eng.HealthCards()
	switch level {
	case "bad":
		return LevelAlert
	case "warn":
		return LevelWarn
	}
	return LevelOK
}

func tokensText(u *store.UsageSum) string {
	total := u.TokensIn + u.TokensOut
	if total == 0 {
		return "—"
	}
	mark := ""
	if u.Estimated {
		mark = "≈ "
	}
	return mark + humanTokens(total)
}

func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f млн", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.0f тыс.", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

// costColor — «без цены» не должно выглядеть как спокойная зелёная сумма:
// это пробел в учёте, а не ноль рублей.
func costColor(u *store.UsageSum, model string, s *Server) string {
	if u.Cost == 0 && model != "" && s.eng.ModelIsRemote(model) && s.eng.PriceOf(model) == "" {
		return "#B25000"
	}
	return "#1E7A32"
}

func costText(u *store.UsageSum, model string, s *Server) string {
	if u.Cost > 0 {
		return fmt.Sprintf("%.2f %s", u.Cost, u.Currency)
	}
	if model != "" && !s.eng.ModelIsRemote(model) {
		return "бесплатно"
	}
	// прочерк молчит, а человек видит его и думает, что вызовы бесплатны.
	// Облачная модель без цены — это не «ноль», это «мы не знаем»
	if model != "" && s.eng.PriceOf(model) == "" {
		return "без цены" // коротко для капсулы; подробности — на «Моделях»
	}
	return "—"
}

// consumerTitle — человеческое имя потребителя и куда ведёт ссылка.
func (s *Server) consumerTitle(key string) (title, kind, target string) {
	k, v, _ := strings.Cut(key, ":")
	switch k {
	case "core":
		return "Ядро · " + workerTitle(v), "core", "tasks"
	case "module":
		if m, ok := s.eng.Module(v); ok {
			return "Модуль «" + m.Title + "»", "module", "modules"
		}
		return "Модуль " + v, "module", "modules"
	case "script":
		return "Скрипт #" + v, "script", "script:" + v
	case "chat":
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			if c, err := s.eng.Store().Chat(id); err == nil {
				return "Чат «" + clipRunes(c.Title, 40) + "»", "chat", "chat:" + v
			}
		}
		return "Чат #" + v, "chat", "chat:" + v
	case "task":
		return "Задача #" + v, "task", "tasks"
	case "assign":
		return "Поручение #" + v, "assign", "tasks"
	case "mcp":
		return "Внешний агент · " + v, "mcp", "memory:sessions"
	}
	return key, k, ""
}

func workerTitle(system string) string {
	for _, t := range []struct{ id, title string }{
		{engine.WorkerPoll, "проверка почты"}, {engine.WorkerArchive, "архив писем"}, {engine.WorkerAttachments, "вложения"},
		{engine.WorkerBackfill, "глубокий разбор"}, {engine.WorkerFolders, "разбор папок"}, {engine.WorkerPurge, "очистка спама"},
		{engine.WorkerMemory, "сведение памяти"}, {engine.WorkerModuleEvents, "события модулей"}, {engine.WorkerFilesMigrate, "переезд файлов"},
		{engine.WorkerPrices, "цены у провайдеров"},
	} {
		if t.id == system {
			return t.title
		}
	}
	if system == "" {
		return "прочее"
	}
	return system
}

func (s *Server) buildHealth(doc *Document, filter string, limit int) error {
	st := s.eng.Store()
	if filter != "" {
		_ = st.SetSetting(healthPeriodKey, filter)
	}
	period := st.Setting(healthPeriodKey, "week")
	since, label := healthSince(period)
	for _, p := range healthPeriods {
		doc.Filters = append(doc.Filters, Filter{ID: p.ID, Title: p.Title})
	}
	doc.ActiveFilter = period
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	cards, level := s.eng.HealthCards()
	doc.Heading = "Здоровье"
	doc.Subheading = "Что с Сёмой и кто сколько съел: каждый вызов модели и запуск кода записан по потребителю"
	if level != "" && level != "ok" {
		doc.Subheading += " · " + levelTitle(level)
	}
	// Тревога и телеметрия — разные вещи, и стоять они должны врозь.
	// До 10 сентября 2026 «наружу ушло 514 отправок, из них 247
	// чувствительных» отличалось от «температура 47 °C» волоском янтарём
	// и словом «внимание» десятым кеглем — то есть ничем. Всё, что не в
	// норме, поднимается полкой над карточками; в карточках остаётся
	// спокойное.
	var cardRows, тревоги []Component
	for _, c := range cards {
		if c.Level != "" && c.Level != "ok" {
			тревоги = append(тревоги, healthAlert(c))
			continue
		}
		// значение и единица врозь: «72 °C» одним куском набиралось тем
		// же кеглем, и единица спорила с самим числом
		значение, единица := valueUnit(c.Value)
		cardRows = append(cardRows, Component{Type: "metric", ID: "health-" + c.Key,
			Title: c.Title, Text: значение, Unit: единица, Caption: c.Meta, Level: cardLevel(c.Level)})
	}
	// профилировщик открыт — пусть это будет видно: забытый, он так и
	// остаётся дверью к памяти процесса (для права «всё», но всё же)
	if s.eng.PprofOn() {
		тревоги = append(тревоги, Component{ID: "health-pprof",
			Subtitle: "Профилировщик включён — /debug/pprof отвечает входу с правом «всё»",
			Caption:  "Выключается в «Работа Сёмы → Экран и связь → Тонкая настройка»",
			Level:    LevelWarn,
			Badges:   []Badge{{Text: levelTitle("warn"), Color: levelColor("warn")}},
			Actions:  []Action{{Action: "$goto", Target: watchScreen + "~" + workTabScreen, Label: "Открыть", Style: "primary"}}})
	}
	if len(тревоги) > 0 {
		add(Component{Type: "shelf", Title: "Стоит посмотреть",
			Meta:     plural(len(тревоги), "запись", "записи", "записей"),
			Children: тревоги})
	}
	if len(cardRows) > 0 {
		add(Component{Type: "cards", Children: cardRows})
	}
	// проверка новой версии: выключена, пока человек не задал адрес и не
	// включил задачу; здесь сказано, что уходит наружу и что ловят суммы
	add(Component{Type: "form", ID: "update-check", Title: "Проверка новой версии",
		Fields: []Field{{Type: "text", Name: "update.url", Label: "Адрес проверки", Value: s.eng.UpdateURL(),
			Hint: "https://… — ответ JSON {\"version\": \"…\"} или просто текст с версией; наружу уходит только номер нынешней сборки, запрос виден на «Что ушло наружу». Пусто — проверки нет. Включается задачей «Проверка новой версии» на «Задачах»"}},
		Submit: &Action{Action: "save_update", Label: "Сохранить"}})
	add(Component{Type: "text", Style: "secondary",
		Text: "Суммы sha256 у сборок и отпечатки образов ловят порчу при скачивании и подмену файла на диске — но не подмену на самом источнике. Перед обновлением всегда копия: откат возможен только вместе с ней."})
	if s.eng.NeedsBinding() {
		add(Component{Type: "row", Layout: "wide", Level: LevelAlert, Dot: levelDot(LevelAlert),
			Subtitle: "Сёма поднят из копии и ждёт привязки",
			Caption:  s.eng.BindingNote() + " · до подтверждения не забирает почту и файлы, не переносит письма и не пишет в зеркало: иначе копия и оригинал отбирали бы письма друг у друга",
			Actions: []Action{{Action: "confirm_binding", Label: "Это его новое место", Style: "primary",
				Confirm: "Оригинал остановлен, ящики, папки и адреса служб проверены? После подтверждения Сёма начнёт забирать почту и файлы.", ConfirmOK: "Подтвердить"}}})
	}
	// версия сборки — чтобы по присланному журналу или скриншоту было
	// видно, что за Сёма стоит у человека
	add(Component{Type: "text", Style: "secondary", Text: "Сборка " + version.String()})

	// остаток на счёте: наш журнал знает, что потратило деньги, но про
	// пополнения ему неоткуда узнать — это спрашивается у провайдера
	if строки := s.balanceRows(); len(строки) > 0 {
		add(Component{Type: "section", Title: "Остаток на счетах", Children: строки})
	}

	// очередь фоновой работы: видно, что Сёма делает не спеша
	if blk := s.queueBlock(); blk != nil {
		add(*blk)
	}
	// что ушло моделям: содержимое, а не только цена
	if blk := s.sentBlock(); blk != nil {
		add(*blk)
	}

	// потребители — по видам: модули, скрипты и задачи, ядро, чаты, агенты.
	// Полоса — доля времени работы (единственный ресурс процесса, который
	// делится по потребителям), капсулы — токены и деньги.
	byConsumer, _ := st.UsageBy("consumer", since, "")
	totalTokens, totalCost, totalSec := 0, 0.0, 0.0
	cur := ""
	for _, u := range byConsumer {
		totalTokens += u.TokensIn + u.TokensOut
		totalCost += u.Cost
		totalSec += u.Seconds
		if u.Currency != "" {
			cur = u.Currency
		}
	}
	// вид потребителя капсулой: список один, и по капсуле видно, кто
	// это — модуль, ядро, чат или внешний агент. Разбивка на секции по
	// видам растягивала экран и прятала главное: кто съел больше всех.
	видНазвание := map[string]string{"module": "модуль", "script": "скрипт",
		"core": "ядро", "chat": "чат", "mcp": "агент"}
	видЦвет := map[string]string{"module": "#0058B0", "script": "#0058B0",
		"core": "#8E8E93", "chat": "#8E8E93", "mcp": "#8E8E93"}
	kindOf := func(key string) string {
		k, _, _ := strings.Cut(key, ":")
		if k == "task" {
			return "script"
		}
		return k
	}
	title := fmt.Sprintf("Кто сколько съел · %s · %s работы · %s токенов", label, humanSeconds(totalSec), humanTokens(totalTokens))
	if totalCost > 0 {
		title += fmt.Sprintf(" · %.2f %s", totalCost, cur)
	}
	// полоса — доля от самого нагруженного потребителя, а не от всей
	// работы: при полусотне строк доля от общего даёт 0,01–0,5 %, и все
	// полосы стоят пустыми, ничего не показывая
	maxSec := 0.0
	for _, u := range byConsumer {
		if u.Seconds > maxSec {
			maxSec = u.Seconds
		}
	}
	var usage []Component
	for _, u := range byConsumer {
		name, _, target := s.consumerTitle(u.Key)
		pct := 0.0
		if maxSec > 0 {
			pct = u.Seconds / maxSec * 100
		}
		facts := []Prop{
			{Label: "Запусков", Value: strconv.Itoa(u.Calls)},
			{Label: "Работы", Value: humanSeconds(u.Seconds)},
		}
		if u.Fails > 0 {
			facts = append(facts, Prop{Label: "Отказов", Value: strconv.Itoa(u.Fails), Color: "#C4271C"})
		}
		if !u.LastAt.IsZero() {
			facts = append(facts, Prop{Label: "Последний", Value: u.LastAt.Format("02.01 15:04")})
		}
		вид := kindOf(u.Key)
		badges := []Badge{{Text: видНазвание[вид], Color: видЦвет[вид]}}
		if badges[0].Text == "" {
			badges = nil
		}
		badges = append(badges, Badge{Text: humanSeconds(u.Seconds), Color: "#8E8E93"})
		if t := tokensText(u); t != "—" {
			badges = append(badges, Badge{Text: t + " токенов", Color: "#0058B0"})
			// доля кэша: по ней видно, окупается ли длинный неизменный
			// кусок промпта — у DeepSeek вход из кэша дешевле в тридцать раз
			if u.Cached > 0 && u.TokensIn > 0 {
				badges = append(badges, Badge{
					Text:  fmt.Sprintf("из кэша %d%%", u.Cached*100/u.TokensIn),
					Color: "#1E7A32"})
			}
		}
		if u.Cost > 0 {
			badges = append(badges, Badge{Text: fmt.Sprintf("%.2f %s", u.Cost, u.Currency), Color: "#1E7A32"})
		}
		row := Component{Type: "row", Layout: "wide", ID: "use-" + u.Key, Subtitle: name,
			Facts: facts, Progress: &pct, Badges: badges}
		if target != "" {
			row.Actions = []Action{{Action: "$goto", Target: target, Label: "Открыть"}}
		}
		usage = append(usage, row)
	}
	if len(usage) == 0 {
		usage = []Component{{Type: "text", Style: "secondary", Text: "За период расхода не было."}}
	}
	// первая порция — самые прожорливые: список отсортирован по расходу, а
	// двадцать шесть строк с полосой и капсулами это 587 узлов
	limit = s.listLimitFor(doc, limit)
	всехПотребителей := len(usage)
	if len(usage) > limit {
		usage = usage[:limit]
	}
	add(Component{Type: "section", Title: title, Children: usage})
	if всехПотребителей > len(usage) {
		add(Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать всех · %d из %d", len(usage), всехПотребителей)}}})
	}
	add(Component{Type: "text", Style: "secondary",
		Text: "Полоса — доля времени работы: память и CPU у Сёмы общие на процесс, по потребителям делится только время. «≈» — токены оценены по длине текста. Деньги — по цене модели; локальные модели бесплатны."})

	// модели
	byModel, _ := st.UsageBy("model", since, "llm")
	var rows []Component
	for _, u := range byModel {
		kind := "локально"
		color := "#1E7A32"
		if s.eng.ModelIsRemote(u.Key) {
			kind, color = "облако", "#8E8E93"
			// цену собираем разбором, а не отрезанием строки по последнему
			// «$»: с тремя ценами в капсулу попадала самая дешёвая — кэш
			if p := s.eng.PriceShort(u.Key); p != "" {
				kind = "облако · " + p
			}
		}
		med := 0.0
		if u.Calls > 0 {
			med = u.Seconds / float64(u.Calls)
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "model-use-" + u.Key, Subtitle: u.Key,
			Caption: fmt.Sprintf("%s · в среднем %.1f с · отказов %d", plural(u.Calls, "вызов", "вызова", "вызовов"), med, u.Fails),
			Badges: []Badge{{Text: kind, Color: color}, {Text: tokensText(u), Color: "#0058B0"},
				{Text: costText(u, u.Key, s), Color: costColor(u, u.Key, s)}}})
	}
	if len(rows) > 0 {
		add(Component{Type: "section", Title: "По моделям", Children: rows})
	}

	// отказы и медленное — при раскрытии: 26 строк с датами и пояснениями
	// это 287 узлов, а смотрят их, когда что-то не так
	add(Component{Type: "section", ID: "health-slow",
		Title:     fmt.Sprintf("Отказы и медленное · дольше %.0f с", s.eng.SlowSeconds()),
		Collapsed: true, Lazy: "health-slow"})

	// процесс за сутки
	if stats, err := st.ProcessStats(time.Now().Add(-24 * time.Hour)); err == nil && len(stats) > 0 {
		last := stats[len(stats)-1]
		var maxRSS, maxDB int64
		maxCPU := 0.0
		for _, p := range stats {
			if p.RSS > maxRSS {
				maxRSS = p.RSS
			}
			if p.CPUPct > maxCPU {
				maxCPU = p.CPUPct
			}
			if p.DBBytes > maxDB {
				maxDB = p.DBBytes
			}
		}
		// мини-графики: последние снимки, прореженные до 60 точек
		pick := func(f func(*store.ProcessStat) float64) []float64 {
			step := len(stats)/60 + 1
			var out []float64
			for i := 0; i < len(stats); i += step {
				out = append(out, f(stats[i]))
			}
			return out
		}
		add(Component{Type: "section", Title: "Процесс · сутки", Children: []Component{
			{Type: "row", Layout: "wide", ID: "proc-rss", Subtitle: "Память процесса", Caption: fmt.Sprintf("сейчас %s · максимум за сутки %s", humanBytes(last.RSS), humanBytes(maxRSS)),
				Spark: pick(func(p *store.ProcessStat) float64 { return float64(p.RSS) })},
			{Type: "row", Layout: "wide", ID: "proc-cpu", Subtitle: "CPU", Caption: fmt.Sprintf("сейчас %.0f%% · пик за сутки %.0f%% · горутин %d", last.CPUPct, maxCPU, last.Goroutines),
				Spark: pick(func(p *store.ProcessStat) float64 { return p.CPUPct })},
			{Type: "row", Layout: "wide", ID: "proc-db", Subtitle: "База", Caption: fmt.Sprintf("%s · WAL %s · максимум %s", humanBytes(last.DBBytes), humanBytes(last.WALBytes), humanBytes(maxDB)),
				Spark: pick(func(p *store.ProcessStat) float64 { return float64(p.DBBytes + p.WALBytes) })},
			{Type: "row", Layout: "wide", ID: "proc-disk", Subtitle: "Хранилище и диск", Caption: fmt.Sprintf("файлы %s · свободно %s · снимков за сутки %d", humanBytes(last.FilesBytes), humanBytes(last.DiskFree), len(stats)),
				Spark: pick(func(p *store.ProcessStat) float64 { return float64(p.FilesBytes) })},
		}})
	}

	// бюджеты
	s.budgetsSection(add)

	// пороги
	var fields []Field
	for _, t := range s.eng.HealthThresholds() {
		fields = append(fields,
			Field{Type: "text", Name: t.Key + ".yellow", Label: t.Title + " · жёлтая", Hint: t.Hint + " · " + t.Unit, Value: trimFloat(t.Yellow)},
			Field{Type: "text", Name: t.Key + ".red", Label: t.Title + " · красная", Hint: t.Unit, Value: trimFloat(t.Red)})
	}
	fields = append(fields,
		Field{Type: "stepper", Name: engine.HealthSlowKey, Label: "Медленная операция", Hint: "попадает в ленту, если дольше", Value: trimFloat(s.eng.SlowSeconds()), Min: 1, Max: 600, Step: 5, Unit: "с"},
		Field{Type: "stepper", Name: engine.HealthKeepKey, Label: "Хранить журнал расхода", Hint: "вызовы моделей построчно: по ним считаются деньги", Value: strconv.Itoa(s.eng.UsageKeepDays()), Min: 7, Max: 365, Step: 1, Unit: "дн."},
		Field{Type: "stepper", Name: engine.HealthKeepRunsKey, Label: "Хранить отметки о запусках", Hint: "каждый проход фонового воркера: строк в сотню раз больше, чем вызовов модели, а денег по ним не считают", Value: strconv.Itoa(s.eng.UsageKeepRunDays()), Min: 1, Max: 365, Step: 1, Unit: "дн."})
	add(Component{Type: "form", ID: "health-thresholds", Title: "Пороги · когда точка в меню жёлтая и красная", Fields: fields,
		Submit: &Action{Action: "save_health", Label: "Сохранить"}})
	return nil
}

// collapseSame — подряд идущие строки с одной и той же причиной
// сворачиваются в одну с счётчиком. Тридцать отказов с одной ошибкой DNS
// занимали весь экран и прятали остальные пять; причина у них общая, а
// поимённо они нужны редко. Порог — сколько повторов уже считается кучей.
func collapseSame(rows []Component, порог int) []Component {
	if порог < 2 {
		порог = 2
	}
	ключ := func(c Component) string {
		if c.Type != "row" {
			return ""
		}
		// причина стоит хвостом пояснения после времени и секунд
		if i := strings.LastIndex(c.Caption, " · "); i >= 0 {
			return c.Subtitle + "|" + c.Caption[i+len(" · "):]
		}
		return c.Subtitle + "|" + c.Caption
	}
	var out []Component
	for i := 0; i < len(rows); {
		k := ключ(rows[i])
		j := i + 1
		for k != "" && j < len(rows) && ключ(rows[j]) == k {
			j++
		}
		if j-i < порог {
			out = append(out, rows[i:j]...)
			i = j
			continue
		}
		name, причина, _ := strings.Cut(k, "|")
		out = append(out, Component{Type: "rowgroup", ID: "гр-" + strconv.Itoa(i),
			Subtitle: name, Caption: причина, Children: append([]Component(nil), rows[i:j]...)})
		i = j
	}
	return out
}

func trimFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func humanSeconds(sec float64) string {
	switch {
	case sec >= 3600:
		return fmt.Sprintf("%.1f ч", sec/3600)
	case sec >= 60:
		return fmt.Sprintf("%.0f мин", sec/60)
	}
	return fmt.Sprintf("%.0f с", sec)
}

var (
	budgetKinds   = [][2]string{{"tokens", "токены"}, {"money", "деньги, $"}}
	budgetPeriods = [][2]string{{"day", "в день"}, {"week", "в неделю"}, {"month", "в месяц"}}
	budgetActions = [][2]string{{"warn", "предупредить"}, {"pause", "пауза"}}
	budgetGroups  = [][2]string{{"module", "все модули"}, {"script", "все скрипты и задачи"}, {"chat", "все чаты"}, {"core", "ядро"}}
)

func pairTitle(pairs [][2]string, key string) string {
	for _, p := range pairs {
		if p[0] == key {
			return p[1]
		}
	}
	return key
}

func pairKey(pairs [][2]string, title string) string {
	for _, p := range pairs {
		if p[1] == title || p[0] == title {
			return p[0]
		}
	}
	return ""
}

// budgetsSection — лимиты расхода: строка с полосой к лимиту и форма.
func (s *Server) budgetsSection(add func(Component)) {
	var rows []Component
	for _, st := range s.eng.BudgetStates() {
		b := st.Budget
		id := strconv.FormatInt(b.ID, 10)
		who := budgetWhoTitle(s, b.Consumer)
		spent := humanTokens(int(st.Spent))
		limit := humanTokens(int(b.Limit)) + " токенов"
		// двадцать триллионов — это «без предела», записанное числом
		if b.Limit >= 1e12 {
			limit = "без предела"
		}
		if b.Kind == "money" {
			spent = fmt.Sprintf("%.2f $", st.Spent)
			limit = fmt.Sprintf("%.2f $", b.Limit)
		}
		caption := fmt.Sprintf("%s %s · съедено %s", limit, pairTitle(budgetPeriods, b.Period), spent)
		color := "#1E7A32"
		switch {
		case st.Exceeded:
			caption += " · превышен"
			color = "#C4271C"
		case st.Pct >= 80:
			color = "#B7791F"
		}
		pct := float64(st.Pct)
		if pct > 100 {
			pct = 100
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "budget-" + id, Subtitle: who, Caption: caption, Progress: &pct,
			Badges: []Badge{{Text: fmt.Sprintf("%d%%", st.Pct), Color: color}, {Text: pairTitle(budgetActions, b.Action), Color: map[bool]string{true: "#C4271C", false: "#0058B0"}[b.Action == "pause"]}},
			Actions: []Action{
				{Action: "set_budget_limit", Target: id, Label: "Изменить", Prompt: "Новый лимит", Value: fmt.Sprintf("%g", b.Limit)},
				{Action: "delete_budget", Target: id, Label: "Удалить", Style: "destructive", Confirm: "Удалить бюджет «" + who + "»?", ConfirmOK: "Удалить"},
			}})
	}
	if len(rows) == 0 {
		rows = []Component{{Type: "text", Style: "secondary", Text: "Бюджетов пока нет: лимит останавливает прожорливого до того, как он съест всё."}}
	}
	add(Component{Type: "section", Title: "Бюджеты · лимиты расхода по потребителям", Children: rows})
	// форма: кому — виды плюс известные потребители
	var whoOptions []string
	for _, g := range budgetGroups {
		whoOptions = append(whoOptions, g[1])
	}
	for _, c := range s.eng.Store().UsageConsumers() {
		if title, _, _ := s.consumerTitle(c); title != "" {
			whoOptions = append(whoOptions, title+" · "+c)
		}
	}
	add(Component{Type: "form", ID: "add-budget", Title: "Новый бюджет", Fields: []Field{
		{Type: "picker", Name: "who", Label: "Кому", Value: whoOptions[0], Options: whoOptions},
		{Type: "picker", Name: "kind", Label: "Что считаем", Value: "токены", Options: []string{"токены", "деньги, $"}},
		{Type: "picker", Name: "period", Label: "Период", Value: "в день", Options: []string{"в день", "в неделю", "в месяц"}},
		{Type: "text", Name: "limit", Label: "Лимит", Hint: "число: токены или доллары", Value: "50000"},
		{Type: "picker", Name: "action", Label: "При превышении", Value: "предупредить", Options: []string{"предупредить", "пауза"}},
	}, Submit: &Action{Action: "add_budget", Label: "Добавить", Style: "primary"}})
	add(Component{Type: "text", Style: "secondary",
		Text: "Проверка идёт перед каждым обращением к модели: превышение — отказ «бюджет исчерпан» в ленте отказов. Пауза снимается руками: включите модуль или задачу обратно, лимит останется."})
}

func budgetWhoTitle(s *Server, consumer string) string {
	if t := pairTitle(budgetGroups, consumer); t != consumer {
		return t
	}
	title, _, _ := s.consumerTitle(consumer)
	return title
}

// screenUsage — расход за сутки и бюджеты, посчитанные один раз на сборку
// экрана. Капсула расхода в строке стоила трёх запросов к журналу: сам
// UsageFor и два обхода бюджетов, каждый со своим UsageFor на каждый бюджет.
// На «Задачах» с двумя десятками строк это шесть десятков запросов и секунда
// ожидания; теперь весь расход берётся одной группировкой, а бюджеты — одним
// проходом. Числа в строках от этого не меняются: сумма та же, считана иначе.
type screenUsage struct {
	perDay  map[string]*store.UsageSum
	budgets []engine.BudgetState
}

// buildUsage — один запрос к журналу и один проход по бюджетам.
func (s *Server) buildUsage() *screenUsage {
	р := &screenUsage{perDay: map[string]*store.UsageSum{}, budgets: s.eng.BudgetStates()}
	rows, err := s.eng.Store().UsageBy("consumer", time.Now().Add(-24*time.Hour), "")
	if err != nil {
		return р
	}
	for _, u := range rows {
		р.perDay[u.Key] = u
	}
	return р
}

// forScreen — сводка потребителя и всех, кто под ним: тот же смысл, что у
// store.UsageFor с приставкой, только без похода в базу.
func (р *screenUsage) forScreen(consumer string) *store.UsageSum {
	out := &store.UsageSum{Key: consumer}
	приставка := consumer + ":"
	for ключ, u := range р.perDay {
		if ключ != consumer && !strings.HasPrefix(ключ, приставка) {
			continue
		}
		out.Calls += u.Calls
		out.Fails += u.Fails
		out.TokensIn += u.TokensIn
		out.TokensOut += u.TokensOut
		out.Cached += u.Cached
		out.Seconds += u.Seconds
		out.Cost += u.Cost
		out.Estimated = out.Estimated || u.Estimated
		if u.Currency > out.Currency {
			out.Currency = u.Currency
		}
		if u.LastAt.After(out.LastAt) {
			out.LastAt = u.LastAt
		}
	}
	return out
}

// budgetBadge — капсула «бюджет N %» для карточек модулей и задач.
func (р *screenUsage) budgetBadge(consumer string) string {
	st := engine.BudgetPick(р.budgets, consumer)
	if st == nil {
		return ""
	}
	if st.Exceeded {
		return "бюджет исчерпан"
	}
	return fmt.Sprintf("бюджет %d%%", st.Pct)
}

// usageBadge — капсула расхода за сутки для карточек модулей и задач.
func (р *screenUsage) usageBadge(consumer string) string {
	u := р.forScreen(consumer)
	b := р.budgetBadge(consumer)
	if u.Calls == 0 {
		return b // расхода нет: показываем бюджет, если он есть
	}
	parts := []string{fmt.Sprintf("за сутки %s", plural(u.Calls, "запуск", "запуска", "запусков"))}
	if t := tokensText(u); t != "—" {
		parts = append(parts, t+" токенов")
	}
	if u.Cost > 0 {
		parts = append(parts, fmt.Sprintf("%.2f %s", u.Cost, u.Currency))
	}
	if u.Fails > 0 {
		parts = append(parts, fmt.Sprintf("отказов %d", u.Fails))
	}
	if b != "" {
		parts = append(parts, b)
	}
	return strings.Join(parts, " · ")
}

func (s *Server) healthActions(req actionRequest) (string, error) {
	switch req.Action {
	case "confirm_binding":
		return "", s.eng.ConfirmBinding()
	case "save_update":
		return "", s.eng.SetUpdateURL(req.Params["update.url"])
	case "add_budget":
		who := req.Params["who"]
		consumer := pairKey(budgetGroups, who)
		if consumer == "" {
			if _, c, ok := strings.Cut(who, " · "); ok {
				consumer = strings.TrimSpace(c)
			}
		}
		if consumer == "" {
			return "", fmt.Errorf("выберите, кому бюджет")
		}
		limit, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(req.Params["limit"]), " ", ""), 64)
		if err != nil || limit <= 0 {
			return "", fmt.Errorf("лимит — положительное число")
		}
		return "", s.eng.Store().AddBudget(&store.Budget{
			Consumer: consumer, Kind: pairKey(budgetKinds, req.Params["kind"]), Period: pairKey(budgetPeriods, req.Params["period"]),
			Limit: limit, Action: pairKey(budgetActions, req.Params["action"]),
		})
	case "set_budget_limit":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		limit, err := strconv.ParseFloat(strings.TrimSpace(req.Params["value"]), 64)
		if err != nil || limit <= 0 {
			return "", fmt.Errorf("лимит — положительное число")
		}
		return "", s.eng.Store().SetBudgetLimit(id, limit)
	case "delete_budget":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		return "", s.eng.Store().DeleteBudget(id)
	case "save_health":
		st := s.eng.Store()
		for k, v := range req.Params {
			if strings.HasPrefix(k, "health.") {
				if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
					return "", fmt.Errorf("«%s»: нужно число", k)
				}
				if err := st.SetSetting(k, strings.TrimSpace(v)); err != nil {
					return "", err
				}
			}
		}
		return "", nil
	}
	return "", errUnhandled
}

// balanceRows — сколько осталось у провайдеров, которые умеют это
// назвать. Умеющих мало, и это честно: гадать по недокументированным
// адресам нельзя — они меняются молча.
// healthAlert — карточка не в норме, поднятая на полку. Название
// показателя и его значение сливаются в одну фразу: «Ушло наружу ·
// сутки» отдельно от «514 отправок» человек читает как две записи.
func healthAlert(c engine.HealthCard) Component {
	name := strings.TrimSpace(strings.TrimSuffix(c.Title, " · сутки"))
	к := Component{
		ID:       "health-" + c.Key,
		Subtitle: name + " — " + c.Value,
		Caption:  c.Meta,
		Level:    cardLevel(c.Level),
		Badges:   []Badge{{Text: levelTitle(c.Level), Color: levelColor(c.Level)}},
	}
	if куда := alertTarget(c.Key); куда != "" {
		к.Actions = []Action{{Action: "$goto", Target: куда, Label: "Открыть", Style: "primary"}}
	}
	return к
}

// alertTarget — куда ведёт разбирательство. Тревога без места, где с ней
// разобраться, — просто испуг.
func alertTarget(key string) string {
	switch key {
	case "sent":
		return sentScreen
	case "queue":
		return "queue"
	case "models", "trust":
		return "settings:models"
	case "mail":
		return "inbox"
	case "events":
		return "modules"
	case "binding":
		return "health"
	case "watch":
		return watchScreen
	}
	return ""
}

func (s *Server) balanceRows() []Component {
	var out []Component
	for _, о := range s.eng.BalancesFromCache() {
		level := LevelOK
		когда := " · спрошено " + whenShort(о.At)
		switch {
		case о.Kind == engine.BalanceEstimate:
			// оценка не спрашивалась: она считается на месте из журнала расхода
			когда = ""
			if о.At.IsZero() {
				level = LevelInfo
			}
		case о.At.IsZero():
			// ещё не спрашивали: это не тревога и не «ноль на счету»
			level = LevelInfo
			когда = " · обновляет задача «Остаток на счетах»"
		}
		switch {
		case о.Err != "":
			level = LevelWarn
		case о.Kind != engine.BalanceEstimate && !о.At.IsZero() && !о.Enough:
			level = LevelAlert
		case о.Low > 0 && len(о.Amounts) > 0 && о.Amounts[0].Total < о.Low:
			level = LevelWarn
		}
		// откуда цифра, написано всегда: «оценка» рядом с числом не даёт
		// принять прикидку за слово провайдера
		out = append(out, Component{Type: "row", Layout: "wide", Level: level, Dot: levelDot(level),
			Subtitle: о.Provider + " · " + о.Label(), Caption: о.Text() + когда,
			Actions: []Action{{Action: "$goto", Target: providerAddr(о.Provider, providerTabBalance), Label: "Настроить"}}})
	}
	return out
}

// failuresFragment — отказы и медленное за выбранный на «Здоровье» период;
// содержимое ленивого раздела.
func (s *Server) failuresFragment(_ string, _ string, _ int) ([]Component, error) {
	st := s.eng.Store()
	since, _ := healthSince(st.Setting(healthPeriodKey, "week"))
	problems, _ := st.UsageProblems(since, s.eng.SlowSeconds(), 30)
	var rows []Component
	for _, r := range problems {
		title, _, target := s.consumerTitle(r.Consumer)
		tag, color := "медленно", "#B7791F"
		if !r.OK {
			tag, color = "отказ", "#C4271C"
		}
		what := r.Model
		if r.Kind == "run" {
			what = "запуск кода"
		}
		row := Component{Type: "row", Layout: "wide", ID: "prob-" + strconv.FormatInt(r.ID, 10),
			Subtitle: title + " · " + what,
			Caption:  fmt.Sprintf("%s · %.1f с · %s", r.At.Format("02.01 15:04"), r.Seconds, clipRunes(r.Note, 120)),
			Badges:   []Badge{{Text: tag, Color: color}}}
		if target != "" {
			row.Actions = []Action{{Action: "$goto", Target: target, Label: "Открыть"}}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = []Component{{Type: "text", Style: "secondary", Text: "Отказов и медленных операций за период нет."}}
	}
	return collapseSame(rows, 3), nil

}

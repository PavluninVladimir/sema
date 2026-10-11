package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
)

// Цена полями — другой вход в ту же строку, а не другая цена: строка, разобранная
// в поля и собранная обратно, считает расход так же, как исходная. Проверяется
// по настоящему costOfUsage на всех формах записи, что бывают в настройках.

// priceForms — формы записи цены из настроек: одна цена, вход и выход, с кэшем,
// с пиком и расписанием, поминутная, в другой валюте.
var priceForms = []struct {
	name, price, peak, schedule string
	perMinute                   bool
}{
	{name: "одна цена", price: "$0.55 / 1M"},
	{name: "вход и выход", price: "$0.55 / $2.20 / 1M"},
	{name: "с кэшем", price: "$0.22 / $0.66 / $0.007 / 1M"},
	{name: "с пиком и расписанием", price: "$0.22 / $0.66 / $0.007 / 1M",
		peak: "$0.44 / $1.32 / $0.014 / 1M", schedule: "пн-пт 01:00-04:00,06:00-10:00 UTC"},
	{name: "пик без кэша", price: "$1 / $4 / 1M", peak: "$2 / $8 / 1M", schedule: "пн-пт 08:00-12:00 UTC"},
	{name: "поминутная", price: "$0.003 / мин", perMinute: true},
	{name: "в рублях", price: "₽120 / ₽480 / 1M"},
}

// Round trip: строка → поля → строка даёт ту же цену, пиковую цену и расписание
// (с точностью до записи числа) и тот же расход для любого вызова.
func TestPricePartsRoundTripKeepsCost(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	usage := llm.Usage{In: 1_000_000, Cached: 600_000, Out: 250_000}
	for _, form := range priceForms {
		parts := PricePartsOfLine(form.price, form.peak, form.schedule)
		line, err := parts.Line()
		if err != nil {
			t.Errorf("%s: поля не собрались: %v", form.name, err)
			continue
		}
		price, peak, schedule, err := parts.Compose()
		if err != nil {
			t.Fatal(err)
		}
		if form.perMinute != parts.IsPerMinute() {
			t.Errorf("%s: поминутность определилась как %v", form.name, parts.IsPerMinute())
		}
		// два конфига: исходный и собранный из полей; расход у них один
		models := []ModelConf{
			{Name: "исходная", Type: "openai", BaseURL: "https://api.example.org/v1", Price: form.price, PricePeak: form.peak, Peak: form.schedule},
			{Name: "из полей", Type: "openai", BaseURL: "https://api.example.org/v1", Price: price, PricePeak: peak, Peak: schedule},
		}
		if err := e.saveUserModels(models); err != nil {
			t.Fatal(err)
		}
		was, wasCur := e.costOfUsage("исходная", usage)
		now, nowCur := e.costOfUsage("из полей", usage)
		if was != now || wasCur != nowCur {
			t.Errorf("%s: расход изменился: было %v %q, стало %v %q (строка %q)", form.name, was, wasCur, now, nowCur, line)
		}
		// цена за минуту звука считается своим путём
		wasAudio, _, _ := e.costOfAudio("исходная", llm.Usage{AudioSec: 90})
		nowAudio, _, _ := e.costOfAudio("из полей", llm.Usage{AudioSec: 90})
		if wasAudio != nowAudio {
			t.Errorf("%s: расход за звук изменился: %v → %v", form.name, wasAudio, nowAudio)
		}
		// пик считается по часам: проверяем оба тарифа независимо от момента теста
		if form.peak != "" {
			for _, at := range []time.Time{
				time.Date(2026, 9, 29, 2, 0, 0, 0, time.UTC),  // вторник, 02:00 UTC
				time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), // вторник, полдень
				time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC),  // суббота, 02:00
			} {
				if inPeak(form.schedule, at) != inPeak(schedule, at) {
					t.Errorf("%s: расписание изменилось при сборке: %q → %q (%s)", form.name, form.schedule, schedule, at)
				}
			}
		}
		// и второй оборот ничего не меняет: запись канонична
		again, err := PricePartsOfLine(price, peak, schedule).Line()
		if err != nil || again != line {
			t.Errorf("%s: повторный оборот дал %q при %q (%v)", form.name, again, line, err)
		}
	}
}

// Поля показывают то, что лежит в строке, без выдуманного: у одной цены выход
// равен входу, кэша нет, пика нет; у поминутной цены — только минута.
func TestPricePartsShowWhatIsStored(t *testing.T) {
	p := PricePartsOfLine("$0.55 / 1M", "", "")
	if p.In != "0.55" || p.Out != "0.55" || p.Cache != "" || p.HasPeak() || p.Currency != "$" {
		t.Errorf("одна цена: %+v", p)
	}
	p = PricePartsOfLine("$0.22 / $0.66 / $0.007 / 1M", "$0.44 / $1.32 / $0.014 / 1M", "пн-пт 01:00-04:00 UTC")
	if p.In != "0.22" || p.Out != "0.66" || p.Cache != "0.007" ||
		p.PeakIn != "0.44" || p.PeakOut != "1.32" || p.PeakCache != "0.014" || p.Schedule != "пн-пт 01:00-04:00 UTC" {
		t.Errorf("с кэшем и пиком: %+v", p)
	}
	p = PricePartsOfLine("$0.003 / мин", "", "")
	if p.Minute != "0.003" || p.In != "" || p.Out != "" || !p.IsPerMinute() {
		t.Errorf("поминутная: %+v", p)
	}
	p = PricePartsOfLine("€2 / €6 / 1M", "", "")
	if p.Currency != "€" {
		t.Errorf("валюта: %q", p.Currency)
	}
	// строка, которую не разобрать, в поля не лезет: пустое честнее выдуманного
	if p = PricePartsOfLine("бесплатно", "", ""); p.In != "" || p.Out != "" || p.Minute != "" {
		t.Errorf("мусор разобрался в числа: %+v", p)
	}
}

// Сборка из полей: что пустое — не названо, запятая в десятичных принимается,
// а ошибки называют поле.
func TestPricePartsCompose(t *testing.T) {
	line := func(p PriceParts) string {
		t.Helper()
		l, err := p.Line()
		if err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		return l
	}
	if got := line(PriceParts{In: "0,55"}); got != "$0.55 / 1M" {
		t.Errorf("одна цена с запятой: %q", got)
	}
	if got := line(PriceParts{In: "0.55", Out: "2.2"}); got != "$0.55 / $2.2 / 1M" {
		t.Errorf("вход и выход: %q", got)
	}
	if got := line(PriceParts{In: "1", Cache: "0.1"}); got != "$1 / $1 / $0.1 / 1M" {
		t.Errorf("кэш при одной цене: %q", got)
	}
	if got := line(PriceParts{Currency: "₽", In: "120", Out: "480"}); got != "₽120 / ₽480 / 1M" {
		t.Errorf("рубли: %q", got)
	}
	if got := line(PriceParts{Minute: "0.003"}); got != "$0.003 / мин" {
		t.Errorf("за минуту: %q", got)
	}
	if got := line(PriceParts{}); got != "" {
		t.Errorf("пустые поля — это «цены нет»: %q", got)
	}
	if got := line(PriceParts{In: "0", Out: "0"}); got != "" {
		t.Errorf("нули — не названо: %q", got)
	}
	peak := line(PriceParts{In: "0.22", Out: "0.66", PeakIn: "0.44", Schedule: "пн-пт 01:00-04:00 UTC"})
	if peak != "$0.22 / $0.66 / 1M | $0.44 / 1M | пн-пт 01:00-04:00 UTC" {
		t.Errorf("пик с одной ценой: %q", peak)
	}

	for name, c := range map[string]struct {
		p    PriceParts
		want string
	}{
		"не число":                   {PriceParts{In: "дорого"}, "«Вход»"},
		"отрицательная":              {PriceParts{In: "-1"}, "«Вход»"},
		"выход без входа":            {PriceParts{Out: "3"}, "«Вход» пуст"},
		"токены и минута":            {PriceParts{In: "1", Minute: "0.003"}, "не обе сразу"},
		"пик у поминутной":           {PriceParts{Minute: "0.003", Schedule: "пн-пт 01:00-04:00 UTC"}, "часы пик"},
		"пик без цены":               {PriceParts{Schedule: "пн-пт 01:00-04:00 UTC"}, "без обычной цены"},
		"пик без расписания":         {PriceParts{In: "1", PeakIn: "2"}, "без расписания"},
		"расписание без цены в пике": {PriceParts{In: "1", Schedule: "пн-пт 01:00-04:00 UTC"}, "цена в нём"},
		"расписание непонятное":      {PriceParts{In: "1", PeakIn: "2", Schedule: "когда-нибудь"}, "расписание не разобрал"},
		"валюта":                     {PriceParts{Currency: "¥", In: "1"}, "валюта"},
	} {
		_, err := c.p.Line()
		if err == nil {
			t.Errorf("%s: принято", name)
			continue
		}
		if !strings.Contains(err.Error(), strings.Split(c.want, " — ")[0]) && !strings.Contains(err.Error(), c.want) {
			// слово из ожидания должно быть в сообщении, иначе человек не поймёт, о каком поле речь
			if name != "расписание без цены в пике" {
				t.Errorf("%s: ошибка %q не называет %q", name, err, c.want)
			}
		}
	}
}

// Цена, сохранённая из полей, ложится тем же SetModelPrice: отметка «вписана
// вами» ставится, когда цена изменилась, и не ставится, когда форма цену не
// тронула — например, поменяли только имя в прайсе.
func TestSetModelPricePartsMarksHumanOnlyWhenChanged(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	if err := e.saveUserModels([]ModelConf{{Name: "м", Type: "openai", BaseURL: "https://api.example.org/v1", Model: "м",
		Price: "$0.55 / $2.20 / 1M", PriceBy: PriceByPrice, PriceAt: time.Now().Add(-48 * time.Hour).Format(time.RFC3339)}}); err != nil {
		t.Fatal(err)
	}
	parts, ok := e.PricePartsOf("м")
	if !ok {
		t.Fatal("модели нет")
	}
	// ту же цену записали заново: ничего не менялось, отметка прайса осталась
	if err := e.SetModelPriceParts("м", parts); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.modelConf("м"); c.PriceBy != PriceByPrice || !strings.HasPrefix(c.Price, "$0.55 / $2.20") {
		t.Errorf("неизменённая цена потеряла отметку прайса: %+v", c)
	}
	// изменили выход — вписано человеком
	parts.Out = "3"
	if err := e.SetModelPriceParts("м", parts); err != nil {
		t.Fatal(err)
	}
	c, _ := e.modelConf("м")
	if c.PriceBy != priceByHuman || c.Price != "$0.55 / $3 / 1M" {
		t.Errorf("изменённая цена: %+v", c)
	}
	// пик добавили полями: ложится в те же три поля конфига
	parts.PeakIn, parts.PeakOut, parts.Schedule = "1", "6", "пн-пт 01:00-04:00 UTC"
	if err := e.SetModelPriceParts("м", parts); err != nil {
		t.Fatal(err)
	}
	c, _ = e.modelConf("м")
	if c.PricePeak != "$1 / $6 / 1M" || c.Peak != "пн-пт 01:00-04:00 UTC" {
		t.Errorf("пик не лёг: %+v", c)
	}
	// пустые поля убирают цену целиком и отметку вместе с ней
	if err := e.SetModelPriceParts("м", PriceParts{}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.modelConf("м")
	if c.Price != "" || c.PriceBy != "" || c.PricePeak != "" {
		t.Errorf("цена не убралась: %+v", c)
	}
	// плохое поле — отказ без записи
	if err := e.SetModelPriceParts("м", PriceParts{In: "x"}); err == nil {
		t.Error("плохое число принято")
	}
	if err := e.SetModelPriceParts("нет-такой", PriceParts{In: "1"}); err == nil {
		t.Error("цена неизвестной модели принята")
	}
}

// Адрес экрана модели строится из её имени: имя, которое читалось бы как адрес
// подключения, провайдера или вкладки, не принимается.
func TestModelNameCannotShadowScreenAddress(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	for _, name := range []string{"new", "provider:OpenAI", "ловушка~prices"} {
		if err := e.AddModel(ModelConf{Name: name, Type: "openai", BaseURL: "https://api.example.org/v1", Model: "m"}); err == nil {
			t.Errorf("имя %q принято", name)
		}
	}
	if err := e.AddModel(ModelConf{Name: "gpt-6-luna", Type: "openai", BaseURL: "https://api.example.org/v1", Model: "m"}); err != nil {
		t.Errorf("обычное имя отвергнуто: %v", err)
	}
}

// Имена ролей в roleNames — те, что в roleDefaults: по ним Roles() решает, нужно
// ли вообще считать умолчания.
func TestRoleNamesMatchDefaults(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	var got []string
	for _, r := range e.roleDefaults() {
		got = append(got, r.Name)
	}
	if strings.Join(got, "|") != strings.Join(roleNames, "|") {
		t.Errorf("roleNames разошёлся с roleDefaults:\n%v\n%v", roleNames, got)
	}
}

// Карточки моделей читаются одним проходом и отвечают на те же вопросы, что
// прежние поштучные: цена, доверие, умение слышать, роли.
func TestModelCardsAnswerPerModelQuestions(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	if err := e.AddModel(ModelConf{Name: "слух", Type: "openai", BaseURL: "https://api.openai.com/v1",
		Model: "gpt-4o-mini-transcribe", Price: "$0.003 / мин"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddModel(ModelConf{Name: "своя", Type: "openai", BaseURL: "http://llm:8080/v1", Model: "q", Trusted: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddModel(ModelConf{Name: "чужая", Type: "openai", BaseURL: "https://gw.example.org/v1", Model: "x"}); err != nil {
		t.Fatal(err)
	}
	cards := map[string]ModelCard{}
	for _, c := range e.ModelCards() {
		cards[c.Name] = c
	}
	if c := cards["слух"]; !c.Hears || !c.HearsByName || c.PriceShort() != "$0.003 / мин" || c.NoPrice() || c.Group != "OpenAI" {
		t.Errorf("слух: %+v", c)
	}
	if c := cards["своя"]; !c.Trusted || c.Remote || c.NoPrice() || c.Group != GroupThisMachine || e.ModelIsTrusted("своя") != c.Trusted {
		t.Errorf("своя: %+v", c)
	}
	if c := cards["чужая"]; c.Trusted || !c.Remote || !c.NoPrice() || c.PriceShort() != "" {
		t.Errorf("чужая: %+v", c)
	}
	for name, c := range cards {
		if want := e.PriceOf(name); strings.TrimSpace(c.Price) != want {
			t.Errorf("%s: цена %q, а у движка %q", name, c.Price, want)
		}
		if hears := e.ModelHears(name); hears != c.Hears {
			t.Errorf("%s: слышит %v, а у движка %v", name, c.Hears, hears)
		}
		if c.PriceShort() != e.PriceShort(name) {
			t.Errorf("%s: короткая цена %q, а у движка %q", name, c.PriceShort(), e.PriceShort(name))
		}
	}
}

package api

// Данные для экранов «Моделей и ролей»: около ста семидесяти моделей у
// четырёх провайдеров, назначенные роли, цены разных форм, одна роль на
// передышке, расход за сутки. Их берут и юнит-тесты, и браузерные, и замеры
// веса, и снимки для ревью. Настоящих провайдеров здесь нет: модели
// подключаются без обращения к сети, а адреса ведут в никуда.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// секреты, которые ни при каких условиях не должны попасть в разметку
const (
	fixKeyOpenAI     = "sk-проба-СЕКРЕТ-openai-0001"
	fixKeyPerplexity = "pplx-проба-СЕКРЕТ-0002"
	fixKeyDeepSeek   = "ds-проба-СЕКРЕТ-0003"
	fixKeyFal        = "fal-проба-СЕКРЕТ-0004"
)

// modelsFixAdd — подключить модель или упасть.
func modelsFixAdd(t *testing.T, s *Server, c engine.ModelConf) {
	t.Helper()
	if err := s.eng.AddModel(c); err != nil {
		t.Fatalf("модель %s: %v", c.Name, err)
	}
}

// modelsFix — сервер с моделями. total — сколько всего моделей подключить
// (с учётом той, что заводит testServer); меньше пятнадцати нельзя: столько
// именованных моделей в фикстуре.
func modelsFix(t *testing.T, total int) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	openai := func(name string) engine.ModelConf {
		return engine.ModelConf{Name: name, Type: "openai", BaseURL: "https://api.openai.com/v1", Model: "openai/" + name}
	}
	pplx := func(name string) engine.ModelConf {
		return engine.ModelConf{Name: name, Type: "perplexity", BaseURL: "https://api.perplexity.ai", Model: "perplexity/" + name, Search: true}
	}
	deepseek := func(name string) engine.ModelConf {
		return engine.ModelConf{Name: name, Type: "openai", BaseURL: "https://api.deepseek.com/v1", Model: name}
	}
	local := func(name, url string) engine.ModelConf {
		return engine.ModelConf{Name: name, Type: "openai", BaseURL: url, Model: name, Trusted: true}
	}

	// именованные: роли, цены, умения
	luna := openai("gpt-6-luna")
	luna.Vision, luna.Search = true, true
	luna.Price, luna.PriceBy, luna.PriceAt = "$2.50 / $10.00 / $0.25 / 1M", engine.PriceByPrice, time.Now().Add(-24*time.Hour).Format(time.RFC3339)
	modelsFixAdd(t, s, luna)
	sol := openai("gpt-6.1-sol")
	sol.Price = "$5 / 1M"
	modelsFixAdd(t, s, sol)
	sol6 := openai("gpt-6-sol")
	sol6.Price, sol6.PriceBy = "$4 / $16 / 1M", "человек"
	modelsFixAdd(t, s, sol6)
	tr := openai("gpt-4o-mini-transcribe")
	tr.Price = "$0.003 / мин"
	modelsFixAdd(t, s, tr)
	modelsFixAdd(t, s, openai("whisper-1"))
	luna5 := openai("gpt-5.6-luna")
	luna5.Search = true
	modelsFixAdd(t, s, luna5)
	modelsFixAdd(t, s, pplx("sonar"))
	fable := pplx("claude-fable-5-1")
	fable.Price = "$3 / $15 / 1M"
	modelsFixAdd(t, s, fable)
	flash := deepseek("deepseek-v4-flash")
	flash.Price = "$0.22 / $0.66 / $0.007 / 1M"
	flash.PricePeak, flash.Peak = "$0.44 / $1.32 / $0.014 / 1M", "пн-пт 01:00-04:00,06:00-10:00 UTC"
	flash.PriceName = "deepseek-v4-flash"
	modelsFixAdd(t, s, flash)
	pro := deepseek("deepseek-v4-pro")
	pro.Price = "$0.55 / $2.20 / 1M"
	modelsFixAdd(t, s, pro)
	modelsFixAdd(t, s, local("qwen3", "http://llm:8080/v1"))
	modelsFixAdd(t, s, local("bge-m3", "http://embed:8080/v1"))

	// остальное — россыпь, как у настоящего каталога: у OpenAI и Perplexity
	// их больше всего, и цены у большинства не названы
	have := 1 + 12 // «тестовая» и названные выше
	for i := 0; have < total; i++ {
		var c engine.ModelConf
		if i%5 < 3 {
			c = openai(fmt.Sprintf("gpt-ряд-%03d", i))
		} else {
			c = pplx(fmt.Sprintf("pplx-ряд-%03d", i))
			c.Search = i%2 == 0
		}
		if i%11 == 0 {
			c.Price = fmt.Sprintf("$%d / 1M", 1+i%7)
		}
		modelsFixAdd(t, s, c)
		have++
	}

	for name, key := range map[string]string{
		"OpenAI": fixKeyOpenAI, "Perplexity": fixKeyPerplexity, "DeepSeek": fixKeyDeepSeek, engine.FalProvider: fixKeyFal,
	} {
		if err := s.eng.SetProviderKey(name, key); err != nil {
			t.Fatal(err)
		}
	}
	// провайдер отдавал список, и в нём нет одной из подключённых моделей —
	// whisper-1 («нет у провайдера»), остальные на месте
	var offered []engine.CatalogModel
	for _, c := range s.eng.ModelCards() {
		if c.Group == "OpenAI" && c.Name != "whisper-1" {
			offered = append(offered, engine.CatalogModel{ID: c.Model})
		}
	}
	if err := s.eng.RememberCatalog("OpenAI", offered); err != nil {
		t.Fatal(err)
	}

	// роли: у каждой есть кому отвечать, у «Перевода» — нет
	for _, r := range []struct{ role, main, spare string }{
		{engine.RoleFilter, "qwen3", "gpt-6-luna"},
		{engine.RoleVision, "gpt-6-luna", "нет"},
		{engine.RoleChat, "gpt-6-luna", "gpt-6.1-sol"},
		{engine.RoleMemory, "gpt-6-luna", "gpt-6.1-sol"},
		{engine.RoleModules, "gpt-6-luna", "gpt-6.1-sol"},
		{engine.RoleSearch, "sonar", "нет"},
		{engine.RoleArbiter, "gpt-6.1-sol", "нет"},
		{engine.RoleJudge, "gpt-6-sol", "claude-fable-5-1"},
		{engine.RoleTranslate, "нет", "нет"},
		{engine.RoleTranscribe, "gpt-4o-mini-transcribe", "нет"},
	} {
		if err := s.eng.SetRoleField(r.role, "model", r.main); err != nil {
			t.Fatalf("роль %s: %v", r.role, err)
		}
		if err := s.eng.SetRoleField(r.role, "spare", r.spare); err != nil {
			t.Fatalf("запасная %s: %v", r.role, err)
		}
	}
	// роль «Память» уже трижды не дождалась ответа и отдыхает
	roles := s.eng.Roles()
	for i := range roles {
		if roles[i].Name == engine.RoleMemory {
			roles[i].LastErr = "gpt-6-luna · 09.09 22:37 · не ответила вовремя"
			roles[i].Last = "gpt-6.1-sol (запасная) · " + time.Now().Format("02.01 15:04")
		}
		if roles[i].Name == engine.RoleChat {
			roles[i].Last = "gpt-6-luna · " + time.Now().Format("02.01 15:04")
		}
	}
	if err := s.eng.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("role.cooldown."+engine.RoleMemory, time.Now().Add(20*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	// расход за сутки у нескольких моделей
	now := time.Now()
	for i, m := range []string{"gpt-6-luna", "gpt-6-luna", "gpt-6.1-sol", "deepseek-v4-flash", "qwen3", "gpt-5.6-luna"} {
		if err := st.AddUsage(&store.UsageRow{At: now.Add(-time.Duration(i+1) * time.Hour), Consumer: "core:poll",
			Kind: "llm", Model: m, TokensIn: 1000 * (i + 1), TokensOut: 300, Seconds: 1.2, OK: true,
			Cost: 0.01 * float64(i+1), Currency: "$"}); err != nil {
			t.Fatal(err)
		}
	}
	return s, st
}

// modelsFixNames — имена всех подключённых моделей.
func modelsFixNames(s *Server) string {
	return strings.Join(s.eng.ProviderNames(), ", ")
}

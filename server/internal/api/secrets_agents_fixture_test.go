package api

// Данные для экранов «Ключей и секретов» и «Агентов»: шесть ваших секретов и
// двадцать служебных, три агента (на связи, со сменившимся отпечатком, шлюз),
// модули, которые просят и получают секреты. Значения секретов — приметные
// строки: проверка «не видно ни на одном экране» ищет их по тексту.

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/ca"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Значения, которых не должно быть ни в одной разметке.
const (
	saTokenSentinel    = "ТОКЕН-PLANNER-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
	saGitlabSentinel   = "ТОКЕН-GITLAB-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
	saPasswordSentinel = "ПАРОЛЬ-ЯНДЕКС-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
	saOtherSentinel    = "СЕКРЕТ-S3-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
	saServiceSentinel  = "СЛУЖЕБНОЕ-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
	saMailSentinel     = "ПАРОЛЬ-ЯЩИКА-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
	saNewValueSentinel = "НОВОЕ-ЗНАЧЕНИЕ-ЗАМЕНЫ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
)

// saFixture — всё, что нужно проверкам, чтобы искать значения и коды.
type saFixture struct {
	S  *Server
	St *store.Store
	// Sentinels — значения, которых не должно быть в разметке: токены, пароли,
	// тела приватных ключей SSH и корня агентов
	Sentinels []string
	// PrivateLines — строки тел приватных ключей PEM (по одной, без заголовков)
	PrivateLines []string
	Root         ca.Pair
}

func privateBodyLines(pem string) []string {
	var out []string
	for _, line := range strings.Split(pem, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-----") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// secretsAgentsFix — данные раздела: шесть ваших секретов, двадцать служебных,
// два сервера, сервер MCP, два модуля, три агента и шлюз.
func secretsAgentsFix(t *testing.T) *saFixture {
	t.Helper()
	s, st := testServer(t)
	fx := &saFixture{S: s, St: st}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// ---- ваши: шесть ----
	for _, name := range []string{"ssh:Microtik", "ssh:srv-vpn"} {
		_, err := s.eng.GenerateSSHKey(name)
		must(err)
		private, ok := st.SecretValue(name)
		if !ok {
			t.Fatalf("не прочитался ключ %s", name)
		}
		fx.PrivateLines = append(fx.PrivateLines, privateBodyLines(private)...)
	}
	must(s.eng.AddSecret("planner-token", store.SecretToken, saTokenSentinel))
	must(s.eng.AddSecret("gitlab-token", store.SecretToken, saGitlabSentinel))
	must(s.eng.AddSecret("yandex-app-password", store.SecretPassword, saPasswordSentinel))
	must(s.eng.AddSecret("s3-secret", store.SecretOther, saOtherSentinel))
	fx.Sentinels = append(fx.Sentinels, saTokenSentinel, saGitlabSentinel, saPasswordSentinel, saOtherSentinel)

	// ---- служебные: двадцать ----
	// корень и сертификат Сёмы для агентов: настоящие, как их заводит ядро
	root, err := s.eng.EnsureRoot()
	must(err)
	fx.Root = root
	_, err = s.eng.EnsureServerCert()
	must(err)
	for _, name := range []string{"agents:root-key", "agents:server-key"} {
		private, ok := st.SecretValue(name)
		if !ok {
			t.Fatalf("не прочитался %s", name)
		}
		fx.PrivateLines = append(fx.PrivateLines, privateBodyLines(private)...)
	}
	for _, sec := range []struct{ name, kind, note string }{
		{"push:vapid-public", store.SecretOther, "открытый ключ пушей (VAPID)"},
		{"push:vapid-private", store.SecretOther, "закрытый ключ пушей (VAPID), наружу не выдаётся"},
		{"tg:api_id", store.SecretToken, "Telegram · api_id"},
		{"tg:api_hash", store.SecretToken, "Telegram · api_hash"},
		{"класс:соль", store.SecretOther, "соль для списка своих значений; сменить её — потерять список"},
		{"класс:свои-значения", store.SecretOther, "солёные хэши личных номеров; самих номеров здесь нет"},
		{"model:OpenAI", store.SecretToken, "ключ провайдера моделей · OpenAI"},
		{"model:DeepSeek", store.SecretToken, "ключ провайдера моделей · DeepSeek"},
		{"model:OpenRouter", store.SecretToken, "ключ провайдера моделей · OpenRouter"},
		{"model:Perplexity", store.SecretToken, "ключ провайдера моделей · Perplexity"},
		{"model:OpenAI:prices", store.SecretToken, "токен источника цен провайдера · OpenAI"},
		{"model:OpenRouter:management", store.SecretToken, "ключ управления провайдера · OpenRouter"},
		{"mcp:sing:oauth", store.SecretOAuth, "вход OAuth на сервер MCP «sing» — им пользуется только ядро"},
		{"mcp:wiki:oauth", store.SecretOAuth, "вход OAuth на сервер MCP «wiki» — им пользуется только ядро"},
	} {
		// без CreatedBy: так их и заводит ядро
		must(st.SaveSecret(&store.Secret{Name: sec.name, Kind: sec.kind, Note: sec.note}, saServiceSentinel+" "+sec.name))
	}
	fx.Sentinels = append(fx.Sentinels, saServiceSentinel)
	// пароли двух ящиков лягут в хранилище сами, как их кладёт «Почта»
	must(st.SaveAccounts([]*store.Account{
		{Name: "проба", Email: "проба@пример.рф", Host: "imap.пример.рф", Port: 993},
		{Name: "owner@example.com", Email: "owner@example.com", Host: "imap.yandex.ru", Port: 993, Password: saMailSentinel},
		{Name: "info@example.org", Email: "info@example.org", Host: "imap.example.org", Port: 993, Password: saMailSentinel},
	}))
	fx.Sentinels = append(fx.Sentinels, saMailSentinel)

	// ---- те, кто пользуется ----
	now := time.Now()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	for _, h := range []*store.Host{
		{Name: "Microtik", Addr: "192.168.1.1", Port: 22, User: "sema", Key: "ssh:Microtik", Kind: store.HostRouterOS,
			Fingerprint: "SHA256:Cq9mXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTMBx3HQBtR"},
		{Name: "srv-vpn", Addr: "203.0.113.12", Port: 22, User: "sema", Key: "ssh:srv-vpn", Kind: store.HostLinux,
			Fingerprint: "SHA256:Bx3HQBtR2m9kXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTM"},
	} {
		must(st.SaveHost(h))
	}
	must(st.SaveMCPServer(&store.MCPServer{Name: "sing", URL: "https://mcp.planner-app.example/mcp", Enabled: true,
		State: "ok", Checked: ago(2 * time.Hour), Secret: "planner-token"}))
	// модуль просит секрет и не получил; другой получил
	must(s.eng.InstallModule(&engine.Module{Name: "gitlab-sync", Title: "Синхронизация GitLab", Needs: []string{"secrets"},
		Secrets: []string{"gitlab-token"}, Code: "function run() { return sema.secret('gitlab-token'); }"}))
	must(s.eng.InstallModule(&engine.Module{Name: "backup", Title: "Резервные копии", Needs: []string{"secrets"},
		Secrets: []string{"yandex-app-password"}, Code: "function run() { return sema.secret('yandex-app-password'); }"}))
	must(s.eng.GrantSecret("backup", "yandex-app-password", true))
	// и модуль просит имя, которого в хранилище нет: «просили, а такого нет»
	st.LogSecret(store.SecretUse{Name: "nextcloud-token", Module: "backup", Who: "модуль «Резервные копии»", Note: "секрета нет"})

	// ---- обращения к секретам ----
	st.LogSecret(store.SecretUse{Name: "ssh:Microtik", Who: "ядро · Microtik · /system print", Granted: true, At: ago(40 * time.Minute)})
	st.LogSecret(store.SecretUse{Name: "ssh:srv-vpn", Who: "ядро · srv-vpn · df -h", Granted: true, At: ago(5 * time.Hour)})
	st.LogSecret(store.SecretUse{Name: "yandex-app-password", Module: "backup", Who: "модуль «Резервные копии»", Granted: true, At: ago(26 * time.Hour)})
	st.LogSecret(store.SecretUse{Name: "gitlab-token", Module: "gitlab-sync", Who: "модуль «Синхронизация GitLab»", Note: "право не выдано", At: ago(3 * time.Hour)})

	// ---- агенты ----
	pub := func(name string) string {
		_, der, err := ca.NewKey()
		must(err)
		cert, err := ca.Sign(root, name, der, 365)
		must(err)
		return cert
	}
	macCert := pub("мак")
	must(st.SaveAgent(&store.Agent{Name: "мак", Role: engine.RoleResident, Cert: macCert, Fingerprint: ca.Fingerprint(macCert),
		Can: []string{"код", "память", "уведомления"}, Allowed: []string{"код", "память"}, State: store.AgentOK,
		Seen: ago(2 * time.Minute), Note: "вошёл по приглашению · роль «житель»"}))
	workCert := pub("рабочая")
	must(st.SaveAgent(&store.Agent{Name: "рабочая", Role: engine.RoleResident, Cert: workCert, Fingerprint: ca.Fingerprint(workCert),
		NewFP: "9f2c:41ab:77d0:e3c1:0b5a:6e18:d4f2:aa90", Can: []string{"код", "память"}, Allowed: []string{"код"},
		State: store.AgentFPBad, Seen: ago(30 * time.Hour)}))
	gwCert := pub("шлюз")
	must(st.SaveAgent(&store.Agent{Name: "шлюз", Role: engine.RoleGateway, Cert: gwCert, Fingerprint: ca.Fingerprint(gwCert),
		Can: []string{engine.CanGateway}, Allowed: []string{engine.CanGateway}, State: store.AgentOK, Seen: ago(3 * time.Hour)}))
	must(st.SetSetting("agents.gateway", "sema.example.org:8767"))
	st.LogAgent(store.AgentUse{Agent: "мак", What: "соединение", OK: true, Detail: ca.Fingerprint(macCert)})
	st.LogAgent(store.AgentUse{Agent: "мак", What: "←память.memory_recall", OK: true, Detail: "ответ 1 КБ"})
	st.LogAgent(store.AgentUse{Agent: "мак", What: "код.agent_status", OK: false, Detail: "агент «мак» сейчас не на связи"})
	st.LogAgent(store.AgentUse{Agent: "рабочая", What: "вход", OK: false, Detail: "отпечаток сменился: 9f2c:41ab:77d0:e3c1"})
	// живой провод — у «мака»: настоящих соединений в тестах нет
	s.linksVia = func() []string { return []string{"мак · на связи 3h12m0s"} }
	return fx
}

// saScreens — все адреса обоих разделов: вкладки, экраны секретов и агентов,
// добавление, приглашение, несуществующие, прежние.
func saScreens(fx *saFixture) []string {
	out := []string{
		secretsScreen, entityAddr(secretsScreen, "", secretsTabCrypt), entityAddr(secretsScreen, "", secretsTabLog),
		entityAddr(secretsScreen, secretsNew, ""), entityAddr(secretsScreen, secretsNew, secretsNewName+"nextcloud-token"),
		entityAddr(secretsScreen, "нет-такого", ""), entityAddr(secretsScreen, "nextcloud-token", secretsTabLog),
		"settings:tools~crypt",
		agentsScreen, entityAddr(agentsScreen, "", agentsTabGateway), entityAddr(agentsScreen, agentsNew, ""),
		entityAddr(agentsScreen, "нет-такого", ""),
	}
	secrets, _ := fx.St.VaultSecrets()
	for _, sec := range secrets {
		for _, tab := range []string{secretsTabList, secretTabGrants, secretsTabLog} {
			out = append(out, entityAddr(secretsScreen, sec.Name, tab))
		}
	}
	agents, _ := fx.St.Agents()
	for _, a := range agents {
		for _, tab := range []string{agentsTabList, agentTabCaps, agentsTabLog} {
			out = append(out, entityAddr(agentsScreen, a.Name, tab))
		}
	}
	return out
}

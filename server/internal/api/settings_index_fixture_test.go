package api

// Данные для индекса настроек: всё, из чего он собирается, в одном сервере —
// сто семьдесят три модели у четырёх провайдеров, пять серверов, двадцать пять
// секретов, два ящика, сервер MCP, три агента, хранилища, скиллы, каналы
// уведомлений, правки человека и служебные отметки в журнале. Сети здесь нет:
// модели подключаются без обращения к ней, живые провода подменены.

import (
	"fmt"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/ca"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Значения, которых не должно быть в индексе и в разметке результатов поиска.
const (
	idxSecretToken    = "ТОКЕН-ИНДЕКС-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-В-ПОИСКЕ"
	idxSecretPassword = "ПАРОЛЬ-ИНДЕКС-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-В-ПОИСКЕ"
	idxSecretService  = "СЛУЖЕБНОЕ-ИНДЕКС-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-В-ПОИСКЕ"
	idxSecretPhrase   = "ФРАЗА-ВХОДА-ИНДЕКС-КОТОРОЙ-НЕТ-В-ПОИСКЕ"
)

// indexSentinels — всё, что нельзя найти в индексе.
func indexSentinels() []string {
	return []string{idxSecretToken, idxSecretPassword, idxSecretService, idxSecretPhrase,
		fixKeyOpenAI, fixKeyPerplexity, fixKeyDeepSeek, fixKeyFal}
}

// indexFix — сервер со всем, что индекс обходит. models — сколько всего моделей.
func indexFix(t *testing.T, models int) (*Server, *store.Store) {
	t.Helper()
	s, st := modelsFix(t, models)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// суд собран: «под судом» и «полная» на серверах доступны
	must(s.eng.SetRoleField(engine.RoleArbiter, "model", "gpt-6.1-sol"))
	must(s.eng.SetRoleField(engine.RoleJudge, "model", "gpt-6-sol"))

	// ---- секреты: шесть ваших и двадцать служебных ----
	for _, name := range []string{"ssh:Microtik", "ssh:srv-vpn"} {
		must(st.SaveSecret(&store.Secret{Name: name, Kind: store.SecretSSH,
			Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIпроба " + name, CreatedBy: store.SecretByHuman}, "приватная часть "+name))
	}
	must(s.eng.AddSecret("planner-token", store.SecretToken, idxSecretToken))
	must(s.eng.AddSecret("yandex-app-password", store.SecretPassword, idxSecretPassword))
	must(s.eng.AddSecret("s3-access", store.SecretPassword, idxSecretPassword))
	must(s.eng.AddSecret("s3-secret", store.SecretOther, idxSecretPhrase))
	for i := 0; i < 20; i++ {
		must(st.SaveSecret(&store.Secret{Name: fmt.Sprintf("mcp:служебный-%02d:oauth", i), Kind: store.SecretOAuth,
			Note: "служебный секрет"}, idxSecretService))
	}

	// ---- серверы ----
	now := time.Now()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	hosts := []*store.Host{
		{Name: "srv-bots", Addr: "203.0.113.26", Port: 22, User: "sema", Key: "ssh:srv-vpn", Kind: store.HostLinux,
			Fingerprint: "SHA256:Bx3HQBtR2m9kXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTM", Autonomy: store.AutonomyFull, Window: "02:00-04:00"},
		{Name: "Microtik", Addr: "192.168.1.1", Port: 22, User: "sema", Key: "ssh:Microtik", Kind: store.HostRouterOS,
			Fingerprint: "SHA256:Cq9mXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTMBx3HQBtR", Autonomy: store.AutonomyFull, Window: "02:00-04:00"},
		{Name: "srv-vpn", Addr: "203.0.113.12", Port: 22, User: "sema", Key: "ssh:srv-vpn", Kind: store.HostLinux,
			Fingerprint: "SHA256:Bx3HQBtR2m9kXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTM", Autonomy: store.AutonomyFull, Window: "02:00-06:00", MaxHour: 5},
		{Name: "WireGuard", Addr: "203.0.113.40", Port: 2222, User: "sema", Key: "ssh:srv-vpn", Kind: store.HostLinux,
			Fingerprint: "SHA256:Dd8mXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTMBx3HQBtR", Autonomy: store.AutonomyCourt},
		{Name: engine.HostOwnerName, Addr: "172.18.0.1", Port: 22, User: "ivan", Key: "ssh:srv-vpn", Kind: store.HostLinux,
			Fingerprint: "SHA256:Ee7mXjP4vWq1Lc8Ya0dHn6sEzUfGi3oT5bTMBx3HQBtR", NoSudo: true},
	}
	for _, h := range hosts {
		must(st.SaveHost(h))
		set := store.RuleSetLinux
		if h.Kind == store.HostRouterOS {
			set = store.RuleSetRouterOS
		}
		_, err := s.eng.TakeRuleSet(h.Name, set)
		must(err)
	}

	// ---- ящики ----
	must(st.SaveAccounts([]*store.Account{
		{Name: "проба", Email: "проба@пример.рф", Host: "imap.пример.рф", Port: 993},
		{Name: "owner@example.com", Email: "owner@example.com", Host: "imap.yandex.ru", Port: 993, Password: idxSecretPassword},
	}))

	// ---- серверы MCP, агенты, хранилища ----
	must(st.SaveMCPServer(&store.MCPServer{Name: "planner", URL: "https://mcp.planner-app.example/mcp", Enabled: true,
		State: "ok", Checked: ago(2 * time.Hour), Secret: "planner-token"}))
	root, err := s.eng.EnsureRoot()
	must(err)
	pub := func(name string) string {
		_, der, err := ca.NewKey()
		must(err)
		cert, err := ca.Sign(root, name, der, 365)
		must(err)
		return cert
	}
	macCert := pub("мак")
	must(st.SaveAgent(&store.Agent{Name: "мак", Role: engine.RoleResident, Cert: macCert, Fingerprint: ca.Fingerprint(macCert),
		Can: []string{"код", "память", "уведомления"}, Allowed: []string{"код", "память"}, State: store.AgentOK, Seen: ago(2 * time.Minute)}))
	gwCert := pub("шлюз")
	must(st.SaveAgent(&store.Agent{Name: "шлюз", Role: engine.RoleGateway, Cert: gwCert, Fingerprint: ca.Fingerprint(gwCert),
		Can: []string{engine.CanGateway}, Allowed: []string{engine.CanGateway}, State: store.AgentOK, Seen: ago(3 * time.Hour)}))
	must(st.SetSetting("agents.gateway", "sema.example.org:8767"))
	s.linksVia = func() []string { return []string{"мак · на связи 3h12m0s"} }
	must(st.AddStorage("yandex", "s3", `{"endpoint":"storage.yandexcloud.net","bucket":"sema-files","region":"ru-central1","prefix":"sema","ssl":true,"access_secret":"s3-access","secret_secret":"s3-secret"}`))

	// ---- уведомления: канал с пушами, адрес входа https ----
	must(st.SetSetting("auth.origin", "https://sema.example.org"))
	must(st.SaveNotifyChannel(&store.NotifyChannel{Kind: "web", Title: "Chrome на маке", Enabled: true,
		MinLevel: "warn", Sources: "servers,mail", QuietFrom: "23:00", QuietTo: "8:00"}))
	must(st.SaveNotifyChannel(&store.NotifyChannel{Kind: "mac", Title: "Mac", Enabled: true, MinLevel: "info"}))

	// ---- Telegram: ключи приложения и вошедшая учётка, чатов нет (в сеть тест не ходит) ----
	must(s.eng.SaveTGCreds("12345", "хэш"))
	must(st.SaveTGAccount(&store.TGAccount{Phone: telegramAccount, Title: "личная", State: "ok", Enabled: true, Every: 1, Batch: 50}))
	return s, st
}

// indexJournal — журнал для проверок: правки человека за месяц (одни параметры
// чаще других), правки с осторожными настройками и служебные отметки.
func indexJournal(t *testing.T, st *store.Store) {
	t.Helper()
	// правки человека подписаны: так их метит `SignChanges`
	edit := func(key, value, who, from string, times int) {
		for i := 0; i < times; i++ {
			mark := st.LastChange()
			v := value
			if i%2 == 1 {
				v = value + "0"
			}
			if err := st.SetSetting(key, v); err != nil {
				t.Fatal(err)
			}
			st.SignChanges(mark, who, from)
		}
	}
	// чаще всех — вход и доступ и самостоятельность серверов: «Под рукой» их не берёт
	edit("auth.phrase_limit", "6", "вы", "settings:access · save_phrase_limit", 9)
	edit("night.max_steps", "8", "вы", "settings:servers~auto · save_night", 8)
	// остальное — то, что можно держать под рукой
	edit("ui.poll_s", "5", "вы", "settings:watch~screen · set_live_settings", 7)
	edit("tg.post_days", "40", "вы", "settings:telegram~answer · save_tg_limits", 6)
	edit("search.save_pages", "2", "вы", "settings:search · save_search_limits", 5)
	edit("chats.command_ttl_h", "30", "вы", "settings:watch~chats · set_chat_settings", 4)
	edit("mcp.chat_calls", "4", "вы", "settings:integrations · save_mcp_limits", 3)
	edit("web.find_per_day", "50", "вы", "settings:search · save_search_limits", 2)
	// служебные отметки Сёмы: подписаны человеком (фоновая запись в долю секунды его
	// действия) и без подписи
	edit("chat.read.71", "10", "вы", "settings:chats · send", 12)
	edit("folders.cursor.vp.INBOX", "100", "", "", 15)
	edit("notify.mac_last_err", "01.10 08:00 · нет связи", "", "", 3)
	edit("tg.history.79990000000.70", "3", "", "", 6)
}

// engineCard — карточка модели движка: короткое имя для карты общих карточек.
type engineCard = engine.ModelCard

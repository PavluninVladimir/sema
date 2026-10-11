package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/ca"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Уведомления про агентов и секреты ведут на их экраны: у каждого агента свой
// экран, «Шифрование» живёт у «Ключей и секретов» (2 октября 2026), и прежнее
// «открыть список» сменилось адресом, который строит один помощник.

func gotoOf(t *testing.T, st *store.Store, key string) string {
	t.Helper()
	list, err := st.Notifications(true, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range list {
		if n.Key != key {
			continue
		}
		for _, a := range n.Actions {
			if a.Kind == "goto" {
				return a.Screen
			}
		}
		t.Fatalf("у уведомления %q нет перехода: %+v", key, n.Actions)
	}
	t.Fatalf("уведомления %q нет", key)
	return ""
}

func TestAgentNotificationsLeadToAgentScreen(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveAgent(&store.Agent{Name: "мак", Fingerprint: "aa:bb", NewFP: "cc:dd", State: store.AgentOK}); err != nil {
		t.Fatal(err)
	}
	// «Агент пропал со связи» — на экран агента, а не в общий список
	e.agentGone("мак", time.Minute)
	if got := gotoOf(t, st, agentOffKey("мак")); got != "settings:agents@мак" {
		t.Errorf("пропажа со связи ведёт на %q", got)
	}
	// сменился отпечаток — тоже на экран агента, где «Было → Стало»
	a, _ := st.Agent("мак")
	e.notifyAgentFP(a)
	if got := gotoOf(t, st, "agent-fp:мак"); got != "settings:agents@мак" {
		t.Errorf("смена отпечатка ведёт на %q", got)
	}
	// новый агент: способности ждут разрешения — сразу на их вкладку
	code, err := e.NewInvite("ноут", RoleResident)
	if err != nil {
		t.Fatal(err)
	}
	_, pub, _ := ca.NewKey()
	if _, _, _, err := e.Enroll(code, pub, []string{"код", "память"}); err != nil {
		t.Fatal(err)
	}
	if got := gotoOf(t, st, "agent:ноут"); got != "settings:agents@ноут~caps" {
		t.Errorf("вход агента ведёт на %q", got)
	}
}

func TestSecretAndAgentScreenAddresses(t *testing.T) {
	cases := map[string]string{
		SecretScreen("", ""):             "settings:secrets",
		SecretScreen("", "crypt"):        "settings:secrets~crypt",
		SecretScreen("ssh:Microtik", ""): "settings:secrets@ssh:Microtik",
		SecretScreen("x", "grants"):      "settings:secrets@x~grants",
		AgentScreen("", ""):              "settings:agents",
		AgentScreen("", AgentTabGateway): "settings:agents~gateway",
		AgentScreen("мак", ""):           "settings:agents@мак",
		AgentScreen("мак", agentTabCaps): "settings:agents@мак~caps",
		AgentScreen("мак", agentTabLog):  "settings:agents@мак~log",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("адрес %q, а ждали %q", got, want)
		}
	}
	// замечание самонаблюдения про ключ шифрования ведёт на «Шифрование»
	if got := lookWhere("Ключ шифрования"); got != "settings:secrets~crypt" {
		t.Errorf("«Ключ шифрования» ведёт на %q", got)
	}
}

// Имя секрета и агента не должно занимать адрес экрана добавления и разрезать
// адрес вкладкой.
func TestReservedNamesRefused(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.AddSecret("new", store.SecretToken, "x"); err == nil || !strings.Contains(err.Error(), "new") {
		t.Errorf("секрет с именем «new»: %v", err)
	}
	if _, err := e.NewInvite("new", RoleResident); err == nil {
		t.Error("приглашение для агента «new»")
	}
	if _, err := e.NewInvite("а~б", RoleResident); err == nil {
		t.Error("приглашение для агента с «~» в имени")
	}
}

// Правка записи секрета — в журнале, а значения в журнале нет; служебный секрет
// не удаляется и не заменяется, а выдача и отзыв модулю пишутся.
func TestSecretChangesLoggedAndServiceProtected(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.AddSecret("мой", store.SecretToken, "значение-один"); err != nil {
		t.Fatal(err)
	}
	if err := e.ReplaceSecret("мой", "значение-два"); err != nil {
		t.Fatal(err)
	}
	m := &Module{Name: "читатель", Title: "Читатель", Needs: []string{"secrets"}, Secrets: []string{"мой"},
		Code: "function run() { return sema.secret('мой'); }"}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	if err := e.GrantSecret("читатель", "мой", true); err != nil {
		t.Fatal(err)
	}
	if err := e.GrantSecret("читатель", "мой", true); err != nil { // повторная выдача — не правка
		t.Fatal(err)
	}
	if err := e.GrantSecret("читатель", "мой", false); err != nil {
		t.Fatal(err)
	}
	log, _ := st.SecretLogFiltered("мой", store.SecretLogChanges, 20)
	var notes []string
	for _, u := range log {
		notes = append(notes, u.Note)
		if strings.Contains(u.Note+u.Who, "значение-") {
			t.Errorf("значение попало в журнал: %+v", u)
		}
	}
	want := "отозван у модуля «Читатель»|выдан модулю «Читатель»|значение заменено|заведён"
	if strings.Join(notes, "|") != want {
		t.Errorf("правки: %v, а ждали %s", notes, want)
	}
	// служебный: завёл не человек
	if err := st.SaveSecret(&store.Secret{Name: "model:X", Kind: store.SecretToken, Note: "ключ провайдера моделей · X"}, "ключ"); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteSecret("model:X"); err == nil {
		t.Error("служебный секрет удалился")
	}
	if err := e.ReplaceSecret("model:X", "иное"); err == nil {
		t.Error("служебный секрет заменился")
	}
	if v, _ := st.SecretValue("model:X"); v != "ключ" {
		t.Errorf("значение служебного секрета изменилось: %q", v)
	}
	// ваш удаляется и уносит выданное
	if err := e.DeleteSecret("мой"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Secret("мой"); ok {
		t.Error("ваш секрет не удалился")
	}
}

// Кто пользуется секретом: один проход даёт серверы, серверы MCP, провайдеры,
// ящики, модули и ядро — и ничего, чего нет.
func TestSecretConsumerIndex(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	for _, name := range []string{"ssh:a", "mcp-key", "model:OpenAI"} {
		kind := store.SecretToken
		if strings.HasPrefix(name, "ssh:") {
			kind = store.SecretSSH
		}
		if err := st.SaveSecret(&store.Secret{Name: name, Kind: kind}, "значение"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveHost(&store.Host{Name: "A", Addr: "192.0.2.1", Port: 22, User: "u", Key: "ssh:a"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "sing", URL: "https://x/mcp", Enabled: true, Secret: "mcp-key"}); err != nil {
		t.Fatal(err)
	}
	idx := e.SecretConsumerIndex()
	got := func(name string) string {
		var out []string
		for _, c := range idx[name] {
			out = append(out, c.Kind+":"+c.Name)
		}
		return strings.Join(out, ",")
	}
	if got("ssh:a") != "host:A" || got("mcp-key") != "mcp:sing" {
		t.Errorf("серверы: %q, %q", got("ssh:a"), got("mcp-key"))
	}
	if got("model:OpenAI") != "provider:OpenAI" {
		t.Errorf("провайдер: %q", got("model:OpenAI"))
	}
	if got("agents:root-key") != "core:agents" || got("push:vapid-private") != "core:push" || got("tg:api_hash") != "core:tg" {
		t.Errorf("ядро: %q %q %q", got("agents:root-key"), got("push:vapid-private"), got("tg:api_hash"))
	}
	if len(idx["никто-не-пользуется"]) != 0 {
		t.Error("у несуществующего секрета нашлись потребители")
	}
	if places := e.SecretPlaces("ssh:a"); len(places) != 1 || places[0] != "сервер «A»" {
		t.Errorf("SecretPlaces: %v", places)
	}
}

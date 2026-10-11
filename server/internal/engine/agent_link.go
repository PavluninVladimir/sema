package engine

// Связь с агентами: свой удостоверяющий центр, приглашения и разбор того,
// кто звонит. Пропуск агента — сертификат, выданный самой Сёмой; общий
// токен в переменной среды пропуском быть перестаёт.
//
// Корневой ключ живёт в хранилище секретов и наружу не выдаётся никогда —
// как приватная часть ключа SSH.

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/ca"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	rootCertSecret = "agents:root-cert"
	rootKeySecret  = "agents:root-key"
	srvCertSecret  = "agents:server-cert"
	srvKeySecret   = "agents:server-key"
	srvNamesKey    = "agents.server_names" // имена, по которым агент узнаёт Сёму
	inviteMinKey   = "agents.invite_min"   // сколько минут живёт приглашение
	certDaysKey    = "agents.cert_days"    // на сколько дней выдаётся сертификат
	agentPortKey   = "agents.listen"       // где слушать агентов
)

// Роли агента. Их две, и они не совмещаются: житель машины читает
// репозитории человека и ходит в память, шлюз стоит лицом к
// интернету. В одном экземпляре это значило бы, что в интернет смотрит
// то, что умеет читать исходники.
//
// Роль закрепляет Сёма при выдаче одноразового кода и помнит рядом с
// отпечатком. Машина роль только называет при знакомстве — и если
// названное расходится с запомненным, это отказ, а не новая правда:
// иначе решение о полномочиях принимала бы она, а не человек.
const (
	RoleResident = "житель"
	RoleGateway  = "шлюз"
)

// CanGateway — способность шлюза: вносить к Сёме запросы из
// интернета. Единственная способность его роли; жителю она не положена.
const CanGateway = "шлюз"

// KnownRole — роль, которую Сёма признаёт. Пусто читается как «житель»:
// удостоверения, выданные до появления ролей, принадлежат жителю — он им и
// был. Незнакомое слово ролью не становится: по нему не выдаётся ничего.
func KnownRole(s string) (string, bool) {
	switch strings.TrimSpace(s) {
	case "", RoleResident:
		return RoleResident, true
	case RoleGateway:
		return RoleGateway, true
	}
	return strings.TrimSpace(s), false
}

// AgentRole — роль по справочнику. Пустая роль в базе — житель, по той же
// причине: агент, заведённый до ролей, ею и был.
func AgentRole(a *store.Agent) string {
	if a == nil {
		return ""
	}
	r, _ := KnownRole(a.Role)
	return r
}

// GatewayAgent — шлюз, если он заведён. Их не может быть несколько:
// адрес шлюза у Сёмы один, и звонить он умеет только по нему.
func (e *Engine) GatewayAgent() (*store.Agent, bool) {
	list, err := e.store.Agents()
	if err != nil {
		return nil, false
	}
	for _, a := range list {
		if AgentRole(a) == RoleGateway {
			return a, true
		}
	}
	return nil, false
}

// EnsureRoot — корень Сёмы. Рождается один раз при первом обращении и
// дальше только читается.
func (e *Engine) EnsureRoot() (ca.Pair, error) {
	// Два одновременных вызова завели бы два корня, и выданные раньше
	// сертификаты разом перестали бы годиться: приёмник поднимается в
	// своей горутине, а приглашение человек выписывает когда захочет.
	e.caMu.Lock()
	defer e.caMu.Unlock()
	return e.ensureRoot()
}

// ensureRoot — то же под уже взятым замком.
func (e *Engine) ensureRoot() (ca.Pair, error) {
	cert, haveCert := e.store.SecretValue(rootCertSecret)
	key, haveKey := e.store.SecretValue(rootKeySecret)
	if haveCert && haveKey && cert != "" && key != "" {
		return ca.Pair{CertPEM: cert, KeyPEM: key}, nil
	}
	pair, err := ca.NewRoot("Сёма", 10)
	if err != nil {
		return ca.Pair{}, err
	}
	// корень хранится как секрет: без ключа шифрования запись отклонится,
	// и это правильно — открытым такое не лежит
	if err := e.store.SaveSecret(&store.Secret{Name: rootCertSecret, Kind: store.SecretOther,
		Note: "корень для агентов · открытая часть"}, pair.CertPEM); err != nil {
		return ca.Pair{}, err
	}
	if err := e.store.SaveSecret(&store.Secret{Name: rootKeySecret, Kind: store.SecretSSH,
		Note: "корень для агентов · закрытый ключ, наружу не выдаётся"}, pair.KeyPEM); err != nil {
		return ca.Pair{}, err
	}
	log.Printf("агенты: заведён корень, отпечаток %s", ca.Fingerprint(pair.CertPEM))
	return pair, nil
}

// RootFingerprint — отпечаток корня для экрана: человек сверяет его
// глазами, как отпечаток сервера.
func (e *Engine) RootFingerprint() string {
	cert, ok := e.store.SecretValue(rootCertSecret)
	if !ok {
		return ""
	}
	return ca.Fingerprint(cert)
}

// InviteMinutes — сколько живёт код приглашения.
func (e *Engine) InviteMinutes() int { return e.settingInt(inviteMinKey, 15) }

// CertDays — на сколько дней выдаётся сертификат агента.
func (e *Engine) CertDays() int { return e.settingInt(certDaysKey, 365) }

// AgentListen — где Сёма слушает агентов; пусто — не слушает вовсе.
func (e *Engine) AgentListen() string { return e.store.SettingText(agentListenDef) }

// NewInvite — одноразовый код для агента. Живёт минуты: секрет, живущий
// вечно в переменной среды, — это и есть то, от чего мы уходим.
//
// Роль выбирает человек здесь и сейчас: она уезжает вместе с кодом и
// закрепляется за агентом при входе. Незнакомое слово — отказ, а не
// «пусть будет житель»: роль решает полномочия, и угадывать её нельзя.
func (e *Engine) NewInvite(name, role string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("нужно имя агента: по нему его будет видно в справочнике")
	}
	// по имени строится адрес экрана агента: «new» занято под «Пригласить
	// агента», а «~» отделяет вкладку
	if name == agentNewName || strings.Contains(name, "~") {
		return "", fmt.Errorf("имя «%s» не подходит: «%s» занято под экран приглашения, а «~» в имени нельзя — по нему строится адрес экрана", name, agentNewName)
	}
	role, ok := KnownRole(role)
	if !ok {
		return "", fmt.Errorf("роль «%s» неизвестна: житель или шлюз", role)
	}
	// Шлюз один: второй звонил бы по тому же адресу, и Сёма не знал
	// бы, кому из них принадлежит соединение. Заменить — через «Забыть».
	if role == RoleGateway {
		if got, exists := e.GatewayAgent(); exists && got.Name != name {
			return "", fmt.Errorf("шлюз уже есть — «%s»; адрес шлюза у Сёмы один, "+
				"поэтому второго не бывает: забудьте прежнего или назовите код его именем", got.Name)
		}
	}
	if _, err := e.EnsureRoot(); err != nil {
		return "", err
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := hex.EncodeToString(raw)
	until := time.Now().Add(time.Duration(e.InviteMinutes()) * time.Minute)
	if err := e.store.AddInvite(code, name, role, until); err != nil {
		return "", err
	}
	e.store.LogAgent(store.AgentUse{Agent: name, What: "приглашение", OK: true,
		Detail: fmt.Sprintf("роль «%s» · код действует %d минут", role, e.InviteMinutes())})
	return code, nil
}

// Enroll — агент пришёл с кодом и своим открытым ключом. Возвращает его
// сертификат, корень (чтобы он мог проверить и нас) и роль — ту, которую
// человек выбрал при выдаче кода. Роль из ответа главнее той, что агент
// просил флагом: код выдавала Сёма, а не его машина.
func (e *Engine) Enroll(code string, pubDER []byte, can []string) (certPEM, rootPEM, role string, err error) {
	// Сначала всё, что может не выйти: корень и разбор ключа. Иначе
	// одноразовый код сгорает на поломке центра или на кривом ключе, и
	// человеку выписывать новый — а злоумышленнику это дешёвый способ
	// сжигать чужие приглашения.
	root, err := e.EnsureRoot()
	if err != nil {
		return "", "", "", err
	}
	if _, err := x509.ParsePKIXPublicKey(pubDER); err != nil {
		return "", "", "", fmt.Errorf("открытый ключ агента не разобран: %w", err)
	}
	inv, ok := e.store.UseInvite(strings.TrimSpace(code))
	if !ok {
		e.store.LogAgent(store.AgentUse{Agent: "неизвестный", What: "вход по приглашению", OK: false,
			Detail: "код не подошёл, просрочен или уже использован"})
		return "", "", "", fmt.Errorf("код не подошёл: он одноразовый и живёт минуты")
	}
	role, _ = KnownRole(inv.Role)
	certPEM, err = ca.Sign(root, inv.Name, pubDER, e.CertDays())
	if err != nil {
		return "", "", "", err
	}
	fp := ca.Fingerprint(certPEM)
	e.agentMu.Lock()
	defer e.agentMu.Unlock()
	a, exists := e.store.Agent(inv.Name)
	if !exists {
		a = &store.Agent{Name: inv.Name}
	}
	a.Role = role
	a.Fingerprint, a.Cert, a.Can, a.State, a.NewFP = fp, certPEM, can, store.AgentOK, ""
	a.Note = "вошёл по приглашению · роль «" + role + "»"
	if err := e.store.SaveAgent(a); err != nil {
		return "", "", "", err
	}
	e.disconnectAgent(a.Name)
	e.store.LogAgent(store.AgentUse{Agent: inv.Name, What: "выдан сертификат", OK: true, Detail: fp})
	log.Printf("агенты: «%s» получил сертификат, отпечаток %s", inv.Name, fp)
	_, _ = e.Notify(&store.Notification{
		Key: "agent:" + inv.Name, Source: "core", Level: "info",
		Title:   "Агент «" + inv.Name + "» подключился",
		Summary: "роль «" + role + "» · отпечаток " + fp + " · способности ждут вашего разрешения",
		Fields: []store.NotifyField{
			{Label: "Роль", Value: role},
			{Label: "Отпечаток", Value: fp},
			{Label: "Умеет", Value: strings.Join(can, ", ")},
		},
		Actions: []store.NotifyAction{
			{Label: "Решить про способности", Kind: "goto", Screen: AgentScreen(inv.Name, agentTabCaps), Style: "primary"},
			{Label: "Понял", Kind: "dismiss"},
		},
	})
	return certPEM, root.CertPEM, role, nil
}

const agentsScreenID = "settings:agents"

// AgentScreen — адрес экрана агента для уведомлений: `settings:agents@имя` и
// вкладка через «~» («caps», «log»; пусто — «Обзор»). Пустое имя — раздел,
// «gateway» в роли вкладки — его «Шлюз». У каждого агента свой экран, и
// уведомление про него ведёт туда, а не в общий список. Одним помощником, как
// ServerScreen у серверов по SSH.
const (
	// вкладки экрана агента и раздела: слова адреса, их же знает экран
	agentTabCaps    = "caps"    // «Способности»
	agentTabLog     = "log"     // «Журнал»
	AgentTabGateway = "gateway" // «Шлюз» у раздела

	// agentNewName — «агент» с этим именем в адресе означает экран приглашения
	agentNewName = "new"
)

func AgentScreen(name, tab string) string {
	addr := agentsScreenID
	if name != "" {
		addr += "@" + name
	}
	if tab != "" {
		addr += "~" + tab
	}
	return addr
}

// AgentAllowed — разрешена ли способность. Неразрешённая не вызывается —
// не «вызывается и отказывает», а не предлагается вовсе.
func (e *Engine) AgentAllowed(name, can string) bool {
	a, ok := e.store.Agent(name)
	if !ok || a.State != store.AgentOK {
		return false
	}
	for _, c := range a.Allowed {
		if c == can {
			return true
		}
	}
	return false
}

// AllowAgent — человек разрешил или забрал способность.
func (e *Engine) AllowAgent(name, can string, on bool) error {
	e.agentMu.Lock()
	defer e.agentMu.Unlock()
	a, ok := e.store.Agent(name)
	if !ok {
		return fmt.Errorf("агента %q нет", name)
	}
	var out []string
	for _, c := range a.Allowed {
		if c != can {
			out = append(out, c)
		}
	}
	if on {
		out = append(out, can)
	}
	a.Allowed = out
	e.store.LogAgent(store.AgentUse{Agent: name, What: "права", OK: true,
		Detail: map[bool]string{true: "разрешено: ", false: "забрано: "}[on] + can})
	if err := e.store.SaveAgent(a); err != nil {
		return err
	}
	if !on {
		e.disconnectAgent(name)
	}
	return nil
}

// RevokeAgent — отзыв доступа. Действует немедленно: сертификат остаётся
// на руках, но Сёма его больше не признаёт.
func (e *Engine) RevokeAgent(name string) error {
	e.agentMu.Lock()
	defer e.agentMu.Unlock()
	a, ok := e.store.Agent(name)
	if !ok {
		return fmt.Errorf("агента %q нет", name)
	}
	a.State, a.Note = store.AgentRevoked, "доступ отозван человеком"
	a.Allowed = nil
	e.store.LogAgent(store.AgentUse{Agent: name, What: "отзыв", OK: true, Detail: "решение человека"})
	log.Printf("агенты: доступ «%s» отозван", name)
	if err := e.store.SaveAgent(a); err != nil {
		return err
	}
	e.disconnectAgent(name)
	return nil
}

func (e *Engine) disconnectAgent(name string) {
	e.linkMu.Lock()
	l := e.links[name]
	e.linkMu.Unlock()
	if l != nil {
		_ = l.conn.Close()
	}
}

// ForgetAgent закрывает и уже открытый провод удалённого агента.
func (e *Engine) ForgetAgent(name string) error {
	e.agentMu.Lock()
	defer e.agentMu.Unlock()
	if err := e.store.DeleteAgent(name); err != nil {
		return err
	}
	e.disconnectAgent(name)
	return nil
}

// AcceptAgentFP — человек принял новый отпечаток после смены сертификата.
func (e *Engine) AcceptAgentFP(name string) error {
	e.agentMu.Lock()
	defer e.agentMu.Unlock()
	a, ok := e.store.Agent(name)
	if !ok {
		return fmt.Errorf("агента %q нет", name)
	}
	if a.NewFP == "" {
		return fmt.Errorf("у «%s» отпечаток не менялся", name)
	}
	a.Fingerprint, a.NewFP, a.State = a.NewFP, "", store.AgentOK
	if a.NewCert != "" {
		// без этого в справочнике остаётся прежний сертификат: срок и
		// подробности потом читаются от старого, а разбираться некому
		a.Cert, a.NewCert = a.NewCert, ""
	}
	a.Note = "новый отпечаток принят человеком"
	e.CloseNotification("agent-fp:"+name, store.NotifyDone, "человек")
	if err := e.store.SaveAgent(a); err != nil {
		return err
	}
	e.disconnectAgent(name)
	return nil
}

// checkCaller — кто звонит: отпечаток предъявленного сертификата против
// справочника. Пропуск здесь, а не в имени: имя можно написать любое.
func (e *Engine) checkCaller(certPEM string) (*store.Agent, error) {
	e.agentMu.Lock()
	defer e.agentMu.Unlock()
	fp := ca.Fingerprint(certPEM)
	if fp == "" {
		return nil, fmt.Errorf("сертификат не разобран")
	}
	if a, ok := e.store.AgentByFingerprint(fp); ok {
		if a.State != store.AgentOK {
			e.store.LogAgent(store.AgentUse{Agent: a.Name, What: "вход", OK: false, Detail: "состояние " + a.State})
			return nil, fmt.Errorf("доступ агента «%s» закрыт", a.Name)
		}
		e.store.TouchAgent(a.Name)
		return a, nil
	}
	// имя знакомое, а отпечаток другой — это тревога, а не переподключение
	name := ca.NameOf(certPEM)
	if a, ok := e.store.Agent(name); ok {
		a.NewFP, a.NewCert, a.State = fp, certPEM, store.AgentFPBad
		_ = e.store.SaveAgent(a)
		e.disconnectAgent(name)
		e.store.LogAgent(store.AgentUse{Agent: name, What: "вход", OK: false, Detail: "отпечаток сменился: " + fp})
		e.notifyAgentFP(a)
		return nil, fmt.Errorf("у агента «%s» сменился отпечаток — нужно ваше решение", name)
	}
	e.store.LogAgent(store.AgentUse{Agent: "неизвестный", What: "вход", OK: false, Detail: fp})
	return nil, fmt.Errorf("этот сертификат Сёма не выдавал")
}

// notifyAgentFP — тревога о смене отпечатка, как у серверов SSH.
func (e *Engine) notifyAgentFP(a *store.Agent) {
	_, _ = e.Notify(&store.Notification{
		Key: "agent-fp:" + a.Name, Source: "core", Level: "alert",
		Title:   "У агента «" + a.Name + "» сменился отпечаток",
		Summary: "соединение отклонено, пока вы не решите",
		Fields: []store.NotifyField{
			{Label: "Было", Value: a.Fingerprint},
			{Label: "Стало", Value: a.NewFP, Level: "alert"},
		},
		Actions: []store.NotifyAction{
			{Label: "Принять новый", Kind: "core", Op: "accept_agent_fp", Ref: a.Name, Style: "primary"},
			{Label: "Отозвать доступ", Kind: "core", Op: "revoke_agent", Ref: a.Name},
			{Label: "Открыть агента", Kind: "goto", Screen: AgentScreen(a.Name, "")},
		},
	})
}

// ServerNames — под какими именами агенты видят Сёму. Меняется настройкой:
// сегодня это домашний адрес, завтра — имя железки в домашней сети.
func (e *Engine) ServerNames() []string {
	raw := e.store.Setting(srvNamesKey, "sema,localhost,127.0.0.1")
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// EnsureServerCert — сертификат самой Сёмы для приёма агентов. Корень для
// этого не годится: у него нет имён, и агент справедливо откажется
// признавать его сервером.
func (e *Engine) EnsureServerCert() (ca.Pair, error) {
	e.caMu.Lock()
	defer e.caMu.Unlock()
	root, err := e.ensureRoot()
	if err != nil {
		return ca.Pair{}, err
	}
	cert, haveCert := e.store.SecretValue(srvCertSecret)
	key, haveKey := e.store.SecretValue(srvKeySecret)
	if haveCert && haveKey && cert != "" && key != "" {
		// сертификат кончается — выпускаем заново, не дожидаясь обрыва;
		// имена разошлись с настройкой — тоже: иначе смена имени сервера
		// молча оставляет агентов без связи, а выглядит как их поломка
		fresh := false
		if until, err := ca.Until(cert); err == nil && time.Until(until) > 14*24*time.Hour {
			fresh = true
		}
		if fresh && sameNames(ca.HostsOf(cert), e.ServerNames()) {
			return ca.Pair{CertPEM: cert, KeyPEM: key}, nil
		}
		if fresh {
			log.Printf("агенты: имена сервера изменились (%s → %s) — перевыпускаю сертификат",
				strings.Join(ca.HostsOf(cert), ", "), strings.Join(e.ServerNames(), ", "))
		}
	}
	pair, err := ca.NewServer(root, "sema", e.ServerNames(), e.CertDays())
	if err != nil {
		return ca.Pair{}, err
	}
	if err := e.store.SaveSecret(&store.Secret{Name: srvCertSecret, Kind: store.SecretOther,
		Note: "сертификат Сёмы для агентов"}, pair.CertPEM); err != nil {
		return ca.Pair{}, err
	}
	if err := e.store.SaveSecret(&store.Secret{Name: srvKeySecret, Kind: store.SecretSSH,
		Note: "ключ Сёмы для агентов, наружу не выдаётся"}, pair.KeyPEM); err != nil {
		return ca.Pair{}, err
	}
	log.Printf("агенты: выпущен сертификат Сёмы на имена %s", strings.Join(e.ServerNames(), ", "))
	return pair, nil
}

// sameNames — совпадают ли наборы имён, порядок не важен.
func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[strings.ToLower(s)] = true
	}
	for _, s := range b {
		if !set[strings.ToLower(s)] {
			return false
		}
	}
	return true
}

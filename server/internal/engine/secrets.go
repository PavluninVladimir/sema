package engine

// Ключи и секреты: ключи SSH, пароли, токены. Значение попадает к модулю
// только по имени и только если человек выдал это имя при установке или
// позже с экрана хранилища. Приватная часть ключа SSH не выдаётся никому —
// ею будет пользоваться примитив ssh ядра, когда появится.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Вкладки раздела «Ключи и секреты», которые называет движок (адреса
// уведомлений и замечаний). Остальные — дело экрана.
const secretTabCrypt = "crypt" // «Шифрование»: ключ SEMA_KEY, «N из N зашифровано», смена ключа

// SecretScreen — адрес экрана «Ключей и секретов» для уведомлений и замечаний:
// `settings:secrets@имя` и вкладка через «~» («grants», «log»; у самого раздела
// «crypt», «log»). Пустое имя — сам раздел. Одним помощником, как ServerScreen:
// склейка адреса руками где-то ещё — это адрес без вкладки, который забудут
// обновить.
func SecretScreen(name, tab string) string {
	addr := "settings:secrets"
	if name != "" {
		addr += "@" + name
	}
	if tab != "" {
		addr += "~" + tab
	}
	return addr
}

// SecretKindTitle — вид секрета словами, как в списке.
func SecretKindTitle(kind string) string {
	switch kind {
	case store.SecretSSH:
		return "ключ SSH"
	case store.SecretToken:
		return "токен"
	case store.SecretPassword:
		return "пароль"
	case store.SecretOAuth:
		return "вход OAuth"
	default:
		return "секрет"
	}
}

var secretName = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._:@-]{0,79}$`)

func checkSecretName(name string) error {
	if !secretName.MatchString(name) {
		return fmt.Errorf("имя секрета — буквы, цифры, точка, двоеточие, дефис; например ssh:prod-1 или gitlab-token")
	}
	// «new» в адресе экрана значит «добавить секрет»: секрет с таким именем
	// остался бы без своего экрана
	if name == secretNewName {
		return fmt.Errorf("имя «%s» занято под экран добавления — назовите секрет иначе", secretNewName)
	}
	return nil
}

// secretNewName — «секрет» с этим именем в адресе экрана означает «Добавить секрет».
const secretNewName = "new"

// Слова журнала секрета о правках записи: их показывает вкладка «Журнал»
// (отбор «Изменения»), по ним же экран секрета находит, когда менялось значение.
const (
	secretChangeAdded   = "заведён"
	secretChangeGranted = "выдан модулю"
	secretChangeRevoked = "отозван у модуля"
	secretChangeDeleted = "удалён"
)

// SecretChangeReplaced — пометка правки «значение заменено»: по ней экран
// секрета отвечает на вопрос «когда менялся».
const SecretChangeReplaced = "значение заменено"

// logSecretChange — записать в журнал секрета правку человека. Значение в
// журнал не попадает никогда: только имя, что сделано и кем.
func (e *Engine) logSecretChange(name, note string) {
	e.store.LogSecret(store.SecretUse{Name: name, Who: "вы", Granted: true, Change: true, Note: note})
}

// AddSecret сохраняет токен, пароль или произвольный секрет.
func (e *Engine) AddSecret(name, kind, value string) error {
	name = strings.TrimSpace(name)
	if err := checkSecretName(name); err != nil {
		return err
	}
	if _, exists := e.store.Secret(name); exists {
		return fmt.Errorf("секрет %q уже есть — замените его значение в списке", name)
	}
	switch kind {
	case store.SecretToken, store.SecretPassword, store.SecretOther:
	default:
		return fmt.Errorf("неизвестный вид секрета %q", kind)
	}
	if err := e.store.SaveSecret(&store.Secret{Name: name, Kind: kind, Note: store.SecretNoteAdded,
		CreatedBy: store.SecretByHuman}, value); err != nil {
		return err
	}
	e.logSecretChange(name, secretChangeAdded)
	return nil
}

// GenerateSSHKey создаёт пару ed25519 и возвращает публичную часть.
// Приватная сразу ложится закрытой; наружу у неё пути нет.
func (e *Engine) GenerateSSHKey(name string) (string, error) {
	name = strings.TrimSpace(name)
	if err := checkSecretName(name); err != nil {
		return "", err
	}
	if _, exists := e.store.Secret(name); exists {
		return "", fmt.Errorf("секрет %q уже есть — удалите его или выберите другое имя", name)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	private := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	public := opensshPublic(pub) + " sema@" + hostName()
	sec := &store.Secret{Name: name, Kind: store.SecretSSH, Public: public, Note: store.SecretNoteGenerated,
		CreatedBy: store.SecretByHuman}
	if err := e.store.SaveSecret(sec, private); err != nil {
		return "", err
	}
	e.logSecretChange(name, secretChangeAdded)
	return public, nil
}

// ImportSSHKey принимает существующий приватный ключ. Это исключение: такой
// ключ уже живёт где-то ещё, и хранилище за него не отвечает.
func (e *Engine) ImportSSHKey(name, private string) (string, error) {
	name = strings.TrimSpace(name)
	if err := checkSecretName(name); err != nil {
		return "", err
	}
	if _, exists := e.store.Secret(name); exists {
		return "", fmt.Errorf("секрет %q уже есть — удалите его или выберите другое имя", name)
	}
	private = strings.TrimSpace(private) + "\n"
	block, _ := pem.Decode([]byte(private))
	if block == nil {
		return "", fmt.Errorf("это не похоже на приватный ключ: ожидается блок -----BEGIN … PRIVATE KEY-----")
	}
	public := ""
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if ed, ok := key.(ed25519.PrivateKey); ok {
			public = opensshPublic(ed.Public().(ed25519.PublicKey)) + " sema@" + hostName()
		}
	}
	sec := &store.Secret{Name: name, Kind: store.SecretSSH, Public: public,
		Note: store.SecretNoteImported, CreatedBy: store.SecretByHuman}
	if err := e.store.SaveSecret(sec, private); err != nil {
		return "", err
	}
	e.logSecretChange(name, secretChangeAdded)
	return public, nil
}

// opensshPublic — публичный ключ в строке для authorized_keys.
func opensshPublic(pub ed25519.PublicKey) string {
	var b []byte
	for _, part := range [][]byte{[]byte("ssh-ed25519"), pub} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(part)))
		b = append(b, part...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b)
}

func hostName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "sema"
	}
	return strings.TrimSuffix(h, ".local")
}

// ReplaceSecret меняет значение, оставляя имя, вид и права.
func (e *Engine) ReplaceSecret(name, value string) error {
	sec, ok := e.store.Secret(name)
	if !ok {
		return fmt.Errorf("секрета %q нет", name)
	}
	if sec.Kind == store.SecretSSH {
		return fmt.Errorf("ключ SSH не заменяется значением — удалите его и сгенерируйте новый")
	}
	if sec.Kind == store.SecretOAuth {
		return fmt.Errorf("токены входа руками не заменяются: выйдите и войдите заново на экране «Интеграции»")
	}
	if sec.Service() {
		return fmt.Errorf("секрет %q заводит и обновляет сам Сёма — он меняется там, где заведён (ключ провайдера у провайдера, пароль ящика у ящика), а не здесь", name)
	}
	if err := e.store.ReplaceSecretValue(name, value); err != nil {
		return err
	}
	e.logSecretChange(name, SecretChangeReplaced)
	return nil
}

// DeleteSecret удаляет секрет и отзывает его у всех модулей. Служебный секрет
// — тот, что завёл сам Сёма, — не удаляется: он живёт у своего владельца (ключ
// провайдера, пароль ящика, корень агентов), а удалённый отсюда он либо
// молча родился бы заново, либо отвалил бы всех агентов разом.
func (e *Engine) DeleteSecret(name string) error {
	sec, existed := e.store.Secret(name)
	if existed && sec.Service() {
		return fmt.Errorf("секрет %q заводит сам Сёма, и удалить его отсюда нельзя: он живёт у своего владельца", name)
	}
	if err := e.store.DeleteSecret(name); err != nil {
		return err
	}
	// после удаления запись о нём остаётся в журнале — единственная
	if existed {
		e.logSecretChange(name, secretChangeDeleted)
	}
	// токены входа на сервер MCP здесь не удаляются: это служебный секрет, и
	// выходят с сервера кнопкой «Выйти» на его экране (MCPLogout), которая
	// сама убирает токены и не оставляет строку сервера врать «вход выполнен»
	all, err := e.modulesMap()
	if err != nil {
		return err
	}
	changed := false
	for _, m := range all {
		if before := len(m.Granted); before > 0 {
			m.Granted = without(m.Granted, name)
			changed = changed || len(m.Granted) != before
		}
	}
	if changed {
		return e.saveModules(all)
	}
	return nil
}

// GrantSecret выдаёт или отзывает у модуля право на имя — без переустановки.
func (e *Engine) GrantSecret(module, name string, on bool) error {
	all, err := e.modulesMap()
	if err != nil {
		return err
	}
	m, ok := all[module]
	if !ok {
		return fmt.Errorf("модуль %q не установлен", module)
	}
	had := m.HasGrant(name)
	m.Granted = without(m.Granted, name)
	if on {
		sec, exists := e.store.Secret(name)
		if !exists {
			return fmt.Errorf("секрета %q нет в хранилище", name)
		}
		if sec.Kind == store.SecretOAuth {
			return fmt.Errorf("токены входа модулю не выдаются: модуль ходит на сервер MCP через sema.mcp.call, а токен держит ядро")
		}
		m.Granted = append(m.Granted, name)
		sort.Strings(m.Granted)
	}
	if err := e.saveModules(all); err != nil {
		return err
	}
	switch {
	case on && !had:
		e.logSecretChange(name, secretChangeGranted+" «"+m.Title+"»")
	case !on && had:
		e.logSecretChange(name, secretChangeRevoked+" «"+m.Title+"»")
	}
	return nil
}

func without(list []string, name string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != name {
			out = append(out, v)
		}
	}
	return out
}

// SecretUsers — модули, которым выдано имя (заголовками, по алфавиту).
func (e *Engine) SecretUsers(name string) []*Module {
	var out []*Module
	for _, m := range e.Modules() {
		for _, g := range m.Granted {
			if g == name {
				out = append(out, m)
			}
		}
	}
	return out
}

// SecretAskers — модули, которые просят имя в манифесте, но не получили его.
func (e *Engine) SecretAskers(name string) []*Module {
	var out []*Module
	for _, m := range e.Modules() {
		if m.HasGrant(name) {
			continue
		}
		for _, s := range m.Secrets {
			if s == name {
				out = append(out, m)
			}
		}
	}
	return out
}

// secretTool — примитив sema.secret(name) для одного запуска модуля.
// Про невыданное и несуществующее отвечает одинаково: модуль не должен
// узнавать, какие секреты у человека есть.
func (e *Engine) secretTool(m *Module, who string) script.Tool {
	return script.Tool{
		Name: "secret", Need: "secrets", Args: "(name)",
		Desc: "секрет из хранилища по выданному имени",
		Fn: func(a []any) (any, error) {
			name := strings.TrimSpace(script.Str(a, 0))
			use := store.SecretUse{Name: name, Module: m.Name, Who: who}
			if !m.HasGrant(name) {
				use.Note = "право не выдано"
				e.store.LogSecret(use)
				return nil, fmt.Errorf("секрет %q модулю не выдан — выдать можно на экране «Ключи и секреты»", name)
			}
			sec, ok := e.store.Secret(name)
			if !ok {
				use.Note = "секрета нет"
				e.store.LogSecret(use)
				return nil, fmt.Errorf("секрет %q модулю не выдан — выдать можно на экране «Ключи и секреты»", name)
			}
			if sec.Kind == store.SecretSSH {
				// приватная часть — только примитиву ssh ядра, а его пока нет
				use.Note = "приватная часть ключа SSH модулям не выдаётся"
				e.store.LogSecret(use)
				return nil, fmt.Errorf("ключ SSH %q модулю не выдаётся: им пользуется только ядро", name)
			}
			if sec.Kind == store.SecretOAuth {
				use.Note = "токены входа модулям не выдаются"
				e.store.LogSecret(use)
				return nil, fmt.Errorf("секрет %q модулю не выдаётся: токеном пользуется только ядро", name)
			}
			value, ok := e.store.SecretValue(name)
			if !ok {
				use.Note = "не расшифровался: проверьте ключ шифрования"
				e.store.LogSecret(use)
				return nil, fmt.Errorf("секрет %q не читается — проверьте ключ шифрования сервера", name)
			}
			use.Granted = true
			e.store.LogSecret(use)
			return value, nil
		},
	}
}

// chatSecretStub — тот же sema.secret для скриптов из чата: секретов не
// даёт, а объясняет, где их можно получить.
func chatSecretStub() script.Tool {
	return script.Tool{
		Name: "secret", Need: "secrets", Args: "(name)",
		Desc: "секрет из хранилища — доступен только модулю с выданным правом, в скрипте из чата его нет",
		Fn: func(a []any) (any, error) {
			return nil, fmt.Errorf("секреты доступны только модулям: оформите это модулем — " +
				"права на каждый секрет подтверждает человек при установке")
		},
	}
}

// KeyedModels — модели, у которых задан ключ: витрина показывает их без
// значений, править их можно только на вкладке «Модели».
func (e *Engine) KeyedModels() []string {
	var out []string
	for _, c := range e.userModels() {
		if strings.TrimSpace(c.APIKey) != "" {
			out = append(out, c.Name)
		}
	}
	return out
}

// RolesUsing — роли, у которых модель основная или запасная.
func (e *Engine) RolesUsing(model string) []string {
	var out []string
	for _, r := range e.Roles() {
		if r.Model == model || r.Spare == model {
			out = append(out, r.Name)
		}
	}
	return out
}

// SecretPlaces — где секрет используется на самом деле. Раньше считались
// только модули, и ключ, которым живут четыре сервера, был подписан
// «никем не используется»: человек мог спокойно его удалить.
func (e *Engine) SecretPlaces(name string) []string {
	var out []string
	for _, c := range e.SecretConsumerIndex()[name] {
		out = append(out, c.Title)
	}
	return out
}

// Кто пользуется секретом: вид потребителя, по которому экран выбирает, куда
// вести человека.
const (
	ConsumerModule   = "module"    // модуль, которому секрет выдан
	ConsumerHost     = "host"      // сервер по SSH: его ключ
	ConsumerMCP      = "mcp"       // сервер MCP: постоянный ключ
	ConsumerMCPLogin = "mcp-login" // вход OAuth на сервер MCP: токены держит ядро
	ConsumerMailbox  = "mailbox"   // ящик почты: пароль приложения
	ConsumerProvider = "provider"  // провайдер моделей: ключ, токен цен, ключ управления
	ConsumerCore     = "core"      // ядро для своего дела: см. CoreWork
)

// Для чего ядро держит секрет само (SecretConsumer.Name у ConsumerCore).
const (
	CoreAgents = "agents" // удостоверяющий центр и сертификат Сёмы для агентов
	CorePush   = "push"   // ключи VAPID пушей в браузер
	CoreTG     = "tg"     // api_id и api_hash Telegram
	CoreOwn    = "own"    // список «своих значений» для класса «секретное»
)

// SecretConsumer — тот, кто пользуется секретом. Модуль — единственный
// потребитель, которому секрет выдаёт человек (Grantable); остальные берут его
// сами и по своему устройству: у сервера это ключ, у ящика пароль.
type SecretConsumer struct {
	Kind string
	// Name — имя сущности для адреса экрана: сервер, модуль, ящик, провайдер;
	// у ядра — слово из CoreAgents и родни
	Name string
	// Title — как назвать человеку: «сервер «Microtik»»
	Title string
}

// Grantable — секрет выдан человеком, и он же может его отозвать.
func (c SecretConsumer) Grantable() bool { return c.Kind == ConsumerModule }

// SecretConsumerIndex — кто чем пользуется, одним проходом по серверам,
// серверам MCP, ящикам, провайдерам и модулям: список из тридцати секретов не
// должен спрашивать всё это тридцать раз. Ключ карты — имя секрета.
//
// Служебные секреты ядра названы поимённо константами, а не приставками: у
// каждого свой владелец, и «agents:*» как маска не сказала бы, что это за
// удостоверяющий центр и куда за ним идти.
func (e *Engine) SecretConsumerIndex() map[string][]SecretConsumer {
	idx := map[string][]SecretConsumer{}
	add := func(secret string, c SecretConsumer) {
		if secret != "" {
			idx[secret] = append(idx[secret], c)
		}
	}
	if hosts, err := e.store.Hosts(); err == nil {
		for _, h := range hosts {
			add(h.Key, SecretConsumer{Kind: ConsumerHost, Name: h.Name, Title: "сервер «" + h.Name + "»"})
		}
	}
	if servers, err := e.store.MCPServers(); err == nil {
		for _, m := range servers {
			add(m.Secret, SecretConsumer{Kind: ConsumerMCP, Name: m.Name, Title: "сервер MCP «" + m.Name + "»"})
			add(mcpOAuthSecret(m.Name), SecretConsumer{Kind: ConsumerMCPLogin, Name: m.Name, Title: "вход на сервер MCP «" + m.Name + "»"})
		}
	}
	for _, a := range e.Accounts() {
		add(store.AccountSecretName(a.Username), SecretConsumer{Kind: ConsumerMailbox, Name: a.Name, Title: "ящик " + a.Name})
	}
	for _, p := range e.Providers() {
		who := SecretConsumer{Kind: ConsumerProvider, Name: p.Name, Title: "провайдер «" + p.Name + "»"}
		add(providerSecret(p.Name), who)
		add(priceTokenSecret(p.Name), who)
		add(managementSecret(p.Name), who)
	}
	for _, m := range e.Modules() {
		for _, g := range m.Granted {
			add(g, SecretConsumer{Kind: ConsumerModule, Name: m.Name, Title: "модуль «" + m.Title + "»"})
		}
	}
	agents := SecretConsumer{Kind: ConsumerCore, Name: CoreAgents, Title: "связь с агентами"}
	for _, n := range []string{rootCertSecret, rootKeySecret, srvCertSecret, srvKeySecret} {
		add(n, agents)
	}
	push := SecretConsumer{Kind: ConsumerCore, Name: CorePush, Title: "пуши в браузер"}
	add(pushPublicSecret, push)
	add(pushPrivateSecret, push)
	tg := SecretConsumer{Kind: ConsumerCore, Name: CoreTG, Title: "Telegram"}
	add(tgAppIDSecret, tg)
	add(tgAppHashSecret, tg)
	own := SecretConsumer{Kind: ConsumerCore, Name: CoreOwn, Title: "свои значения для класса «секретное»"}
	add(saltSecret, own)
	add(ownValuesSecret, own)
	add(shareKeySecret, SecretConsumer{Kind: ConsumerCore, Name: CoreShare, Title: "ссылки на файлы"})
	return idx
}

package store

// Индивидуальные настройки ящика: как в него входим и что делаем с папками.
// Раньше это жило в config.yaml на весь сервер — теперь у каждого ящика своё,
// потому что у Gmail вход через Google, у Mail.ru пароль приложения, а папки
// («Архив», «Спам») называются везде по-разному.

import "encoding/json"

// Типы авторизации: у каждого свой набор полей в настройках.
const (
	AuthGoogle      = "google"       // OAuth: client id + secret, пароля нет
	AuthAppPassword = "app_password" // пароль приложения, хост из пресета
	AuthIMAP        = "imap"         // свой сервер: хост, порт, логин, пароль
	AuthExchange    = "exchange"     // OAuth Microsoft: + tenant
)

type MailConf struct {
	Auth     string `json:"auth"`
	ClientID string `json:"client_id"`
	Secret   string `json:"secret"`
	Tenant   string `json:"tenant"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`

	// отправка. Пароль здесь не хранится: только имя секрета из хранилища
	// («Ключи и секреты»), а значение читает ядро в момент отправки
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	SMTPUser     string `json:"smtp_user,omitempty"`
	SMTPSecret   string `json:"smtp_secret,omitempty"`
	SMTPStartTLS bool   `json:"smtp_starttls,omitempty"`

	Archive string   `json:"archive"` // папка, в которую складываем разобранное
	Junk    string   `json:"junk"`    // папка спама
	Skip    []string `json:"skip"`    // папки, которые не разбираем
	Folders []string `json:"folders"` // список папок с сервера, прочитан при входе

	FetchDays int `json:"fetch_days"` // за сколько дней просматривать «Входящие»
	JunkDays  int `json:"junk_days"`  // через сколько дней чистить спам

	// Сроки удаления у каждого ящика свои. Ноль и nil значат «как у
	// всех»: общая настройка остаётся умолчанием, а не законом. У
	// рабочего ящика переписку не стирают вовсе, у личного спам живёт
	// две недели — одним числом на оба это не описать.
	KeepDays   int   `json:"keep_days,omitempty"`   // через сколько дней стирать обычные письма
	PurgeOther *bool `json:"purge_other,omitempty"` // стирать ли их вообще

	// Назначение и режим: рабочий ящик по умолчанию закрыт для облачной
	// модели и не даёт чеков; в режиме «только читать» Сёма ничего не
	// меняет на сервере. Галочки сбора и «наружу» — nil, пока человек не
	// решил сам: тогда действует умолчание по назначению.
	Purpose          string `json:"purpose,omitempty"`           // personal | work
	Mode             string `json:"mode,omitempty"`              // tidy | readonly
	CollectReceipts  *bool  `json:"collect_receipts,omitempty"`  // старое поле, читается ради прежних настроек
	CollectAttention *bool  `json:"collect_attention,omitempty"` // старое поле, читается ради прежних настроек
	// Collect — галочки «что собирать из ящика» по имени признака. Имя
	// придумывает тот, кто признак объявил: ядро для своих, модуль для
	// своих. Раньше здесь было два поля с именами модулей, и ядро знало
	// про «чеки» и «внимание» больше, чем имело право.
	Collect  map[string]*bool `json:"collect,omitempty"`
	Outbound *bool            `json:"outbound,omitempty"`

	Connected string `json:"connected"` // когда последний раз удался вход
	LastErr   string `json:"last_err"`  // и что мешает, если не удался
	Tasks     []int64
}

// Назначение ящика и режим работы с сервером.
const (
	PurposePersonal = "personal"
	PurposeWork     = "work"
	ModeTidy        = "tidy"     // читать и наводить порядок
	ModeReadOnly    = "readonly" // только читать: на сервере ничего не меняется
)

// IsWork — рабочая ли это почта.
func (c *MailConf) IsWork() bool { return c.Purpose == PurposeWork }

// ReadOnly — запрещены ли переносы, пометки и очистка на сервере.
func (c *MailConf) ReadOnly() bool { return c.Mode == ModeReadOnly }

// CollectAttentionName — признак ядра «Уведомления из писем»: его держит
// задача «Сроки», а не модуль.
const CollectAttentionName = "attention"

// CollectOn — включён ли признак сбора у этого ящика. Человек решения не
// принимал — действует умолчание того, кто признак объявил. Настройки,
// сделанные до переезда на карту, читаются из старых полей.
func (c *MailConf) CollectOn(name string, def bool) bool {
	if c == nil {
		return def
	}
	if v, ok := c.Collect[name]; ok && v != nil {
		return *v
	}
	switch name {
	case "receipts":
		if c.CollectReceipts != nil {
			return *c.CollectReceipts
		}
	case "attention":
		if c.CollectAttention != nil {
			return *c.CollectAttention
		}
	}
	return def
}

// SetCollect — решение человека по признаку сбора.
func (c *MailConf) SetCollect(name string, on bool) {
	if c.Collect == nil {
		c.Collect = map[string]*bool{}
	}
	v := on
	c.Collect[name] = &v
	// старые поля держим в согласии с картой, пока их кто-то читает
	switch name {
	case "receipts":
		c.CollectReceipts = &v
	case "attention":
		c.CollectAttention = &v
	}
}

// AttentionOn — извлекать ли сроки и просьбы; признак ядра, его держит
// задача «Сроки». Модуль «Требует внимания» снесён 3 сентября 2026, а
// признак остался за ядром — поэтому имя здесь законно.
func (c *MailConf) AttentionOn() bool { return c.CollectOn(CollectAttentionName, true) }

// OutboundOn — можно ли отдавать письма ящика облачной модели; рабочий
// ящик по умолчанию закрыт.
func (c *MailConf) OutboundOn() bool { return boolOr(c.Outbound, !c.IsWork()) }

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Skipped — не разбираем ли эту папку.
func (c *MailConf) Skipped(folder string) bool {
	for _, s := range c.Skip {
		if s == folder {
			return true
		}
	}
	return false
}

// SetSkipped включает и выключает разбор папки.
func (c *MailConf) SetSkipped(folder string, skip bool) {
	out := c.Skip[:0]
	for _, s := range c.Skip {
		if s != folder {
			out = append(out, s)
		}
	}
	c.Skip = out
	if skip {
		c.Skip = append(c.Skip, folder)
	}
}

const mailConfKey = "mailconf.json"

func (s *Store) mailConfs() map[string]*MailConf {
	out := map[string]*MailConf{}
	_ = json.Unmarshal([]byte(s.Setting(mailConfKey, "{}")), &out)
	return out
}

// MailConf — настройки ящика; для незнакомого возвращается пустой набор,
// который вызывающий заполняет значениями из config.yaml.
func (s *Store) MailConf(account string) *MailConf {
	if c, ok := s.mailConfs()[account]; ok && c != nil {
		c.Secret = s.open(c.Secret)
		c.Password = s.open(c.Password)
		return c
	}
	return &MailConf{}
}

func (s *Store) SaveMailConf(account string, c *MailConf) error {
	all := s.mailConfs()
	closed := *c
	closed.Secret = s.seal(c.Secret)
	closed.Password = s.seal(c.Password)
	all[account] = &closed
	raw, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return s.SetSetting(mailConfKey, string(raw))
}

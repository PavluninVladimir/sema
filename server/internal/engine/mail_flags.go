package engine

import "github.com/PavluninVladimir/sema/server/internal/store"

// Признаки сбора у ящика («что из него собирать»). Раньше ядро знало их
// поимённо — «receipts» и «attention» — хотя оба были именами модулей:
// один снесён, второго нет вовсе, а галочка в настройках ящика осталась
// и включала то, чего нет. Теперь признак объявляет тот, кто его
// исполняет: ядро — свои, модуль — свои, в манифесте.

// MailFlag — признак сбора: имя, подпись человеку, умолчание и кто его
// исполняет («ядро» или имя модуля).
type MailFlag struct {
	Name    string
	Title   string
	Module  string // пусто — признак ядра
	Default func(c *store.MailConf) bool
}

// On — включён ли признак у этого ящика.
func (f MailFlag) On(c *store.MailConf) bool {
	def := true
	if f.Default != nil && c != nil {
		def = f.Default(c)
	}
	return c.CollectOn(f.Name, def)
}

// MailFlags — все признаки сбора: ядра и установленных модулей. Модуль
// выключен или удалён — признака нет, и галочка не показывается: она
// включала бы то, чего нет.
func (e *Engine) MailFlags() []MailFlag {
	flags := []MailFlag{{
		Name:  store.CollectAttentionName,
		Title: "Уведомления из писем",
		// признак ядра: его держит задача «Сроки»
		Default: func(*store.MailConf) bool { return true },
	}}
	for _, m := range e.Modules() {
		if m.Flag == "" || !m.Enabled {
			continue
		}
		if m.Flag == store.CollectAttentionName {
			continue // признак ядра модулю не переопределить
		}
		flags = append(flags, MailFlag{
			Name:    m.Flag,
			Title:   flagTitle(m),
			Module:  m.Name,
			Default: flagDefault(m.FlagDefault),
		})
	}
	return flags
}

// MailFlagByName — признак по имени; нет такого — второй ответ false.
func (e *Engine) MailFlagByName(name string) (MailFlag, bool) {
	for _, f := range e.MailFlags() {
		if f.Name == name {
			return f, true
		}
	}
	return MailFlag{}, false
}

func flagTitle(m *Module) string {
	if m.FlagTitle != "" {
		return m.FlagTitle
	}
	if m.Title != "" {
		return m.Title
	}
	return m.Name
}

func flagDefault(spec string) func(*store.MailConf) bool {
	switch spec {
	case "off":
		return func(*store.MailConf) bool { return false }
	case "personal":
		// у рабочего ящика по умолчанию не собираем
		return func(c *store.MailConf) bool { return !c.IsWork() }
	default:
		return func(*store.MailConf) bool { return true }
	}
}

// collectFlags — что собирать из этого ящика, для события модулю. Ящик
// неизвестен (файл не из письма) — действуют умолчания.
func (e *Engine) collectFlags(c *store.MailConf) map[string]bool {
	out := map[string]bool{}
	for _, f := range e.MailFlags() {
		out[f.Name] = f.On(c)
	}
	return out
}

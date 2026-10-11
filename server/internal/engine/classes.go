package engine

// Классы чувствительности у писем и файлов. Ставка на облачные модели
// значит, что личное должно оставаться дома: секретное наружу не уходит
// вовсе, чувствительное — только с согласия человека, и он видит, что
// именно вырезано.

import (
	"fmt"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	sensitiveModeKey = "audit.sensitive" // ask | allow
	consentSender    = "sender"
)

// SensitiveMode — что делать с чувствительным для облачной модели.
func (e *Engine) SensitiveMode() string {
	if v := e.store.Setting(sensitiveModeKey, "allow"); v == "ask" {
		return "ask"
	}
	return "allow"
}

func (e *Engine) SetSensitiveMode(mode string) error {
	if mode != "ask" {
		mode = "allow"
	}
	return e.store.SetSetting(sensitiveModeKey, mode)
}

// SnapshotExclusion — чего не бывает в сборке, которая ляжет на устройство:
// закрытых для облака ящиков и записей класса «секретное». Одна точка на всю
// программу, и она всегда возвращает не-nil: признаком снимка была сама карта
// закрытых ящиков, а её нет вовсе, когда закрытых ящиков нет, — и тогда
// «секретное» уезжало на диск телефона так же, как обычное письмо.
func (e *Engine) SnapshotExclusion() *store.Exclusion {
	return &store.Exclusion{Accounts: e.ClosedAccounts(true), Secret: true}
}

// ClassOfMessage — класс письма. Считается один раз по теме и выжимке и
// запоминается: пометка человека потом важнее и не пересчитывается.
//
// Письмо приходит сюда и до вставки в базу — разметка спама спрашивает
// класс раньше, чем у письма появится номер. Тогда класс считается, но не
// запоминается: строка с id = 0 склеила бы все неразмеченные письма в одну.
func (e *Engine) ClassOfMessage(m *store.Message) string {
	if m.ID > 0 {
		if class, ok := e.store.ContentClass(store.ClassMsg, m.ID); ok && class != "" {
			return class
		}
	}
	class, why := sentClassWhy(m.Subject + "\n" + m.Snippet + "\n" + m.FromAddr)
	if m.ID > 0 {
		_ = e.store.SetContentClass(store.ClassMsg, m.ID, class, "", why)
	}
	return class
}

// ClassOfFile — класс файла по имени и извлечённому тексту.
func (e *Engine) ClassOfFile(a *store.Attachment) string {
	if a.ID > 0 {
		if class, ok := e.store.ContentClass(store.ClassFile, a.ID); ok && class != "" {
			return class
		}
	}
	class, why := sentClassWhy(a.Filename + "\n" + clip(a.Extracted, 2000))
	if a.ID > 0 {
		_ = e.store.SetContentClass(store.ClassFile, a.ID, class, "", why)
	}
	return class
}

// MarkClass — пометка человеком: она главнее приметы. Причину затираем
// пустой: примета тут больше ни при чём, решение принял человек.
func (e *Engine) MarkClass(kind string, id int64, class, by string) error {
	switch class {
	case store.ClassPlain, store.ClassSensitive, store.ClassSecret:
	default:
		return fmt.Errorf("класс бывает обычное, чувствительное или секретное")
	}
	return e.store.SetContentClass(kind, id, class, by, "")
}

// outsideCut — вырезать ли эту запись из собранного контекста. Это не
// дверь: дверь одна и стоит у выхода (`ПередМоделью`), а здесь решается
// судьба одной сущности — письма, файла, картинки, — чтобы модель получила
// контекст без неё и честную строку о том, что именно скрыто.
// Возвращает: отдавать ли, и если нет — почему.
func (e *Engine) outsideCut(cloud bool, class, sender string) (give bool, why string) {
	if !cloud {
		return true, ""
	}
	// класса нет — значит его не считали или посчитать не вышло. Молчание
	// классификатора не разрешение: раньше пустой класс проходил как
	// «обычное», и тринадцать файлов из пятнадцати с приметами документов
	// уезжали наружу именно так — просто потому, что их никто не смотрел
	if class == "" {
		class = store.ClassSensitive
	}
	if class == store.ClassPlain {
		return true, ""
	}
	if class == store.ClassSecret {
		return false, "секретное"
	}
	// чувствительное: по умолчанию отдаём, в режиме «спрашивать» — только с
	// согласия человека по этому отправителю
	if e.SensitiveMode() == "allow" {
		return true, ""
	}
	if allowed, decided := e.store.Consent(consentSender, sender); decided && allowed {
		return true, ""
	}
	return false, "чувствительное"
}

// cutNotice — строка модели о том, что часть контекста вырезана. Молчать
// нельзя: иначе модель уверенно ответит по неполным данным.
//
// Слов-примет в самой строке быть не должно: она входит в промпт, а дверь
// (`BeforeModel`) читает промпт целиком. «Паспорт, медицина» и «личным» в
// пояснении поднимали класс всего промпта до секретного, и облачная модель
// получала отказ двери на собственную же пометку «часть скрыта от вас» — то
// есть честный ответ про скрытое был невозможен ровно там, где он нужен.
func cutNotice(secret, sensitive int, senders []string) string {
	if secret == 0 && sensitive == 0 {
		return ""
	}
	var parts []string
	if secret > 0 {
		parts = append(parts, fmt.Sprintf("%d записей класса «секретное»", secret))
	}
	if sensitive > 0 {
		parts = append(parts, fmt.Sprintf("%d записей класса «чувствительное» без согласия человека", sensitive))
	}
	out := "\nWithheld from you (you are a cloud model): " + strings.Join(parts, ", ") +
		". Say so honestly if the question is about them; do not guess their content."
	if len(senders) > 0 {
		out += " Sources: " + strings.Join(firstN(senders, 5), ", ") + "."
	}
	return out + "\n"
}

// notifyConsent — просьба разрешить отправителя. Один вопрос на отправителя,
// а не на каждое письмо.
func (e *Engine) notifyConsent(sender string) {
	if sender == "" {
		return
	}
	if _, decided := e.store.Consent(consentSender, sender); decided {
		return
	}
	_, _ = e.Notify(&store.Notification{
		Key: "consent:" + sender, Source: "core", Level: "info",
		Title:   "Отдавать облачной модели письма от " + sender + "?",
		Summary: "в них есть чувствительное: суммы, счета, личные данные. Сейчас они вырезаются из контекста",
		Fields:  []store.NotifyField{{Label: "Отправитель", Value: sender}},
		Actions: []store.NotifyAction{
			{Label: "Разрешить", Kind: "core", Op: "grant_class", Ref: sender, Arg: "allow", Style: "primary"},
			{Label: "Не отдавать", Kind: "core", Op: "grant_class", Ref: sender, Arg: "deny"},
		},
	})
}

// DecideConsent — решение человека по отправителю.
func (e *Engine) DecideConsent(sender string, allow bool) error {
	if err := e.store.SetConsent(consentSender, sender, allow, "человек"); err != nil {
		return err
	}
	e.CloseNotification("consent:"+sender, store.NotifyDone, "человек")
	return nil
}

// reasonNoTrusted — причина у письма, которое осталось без разметки:
// закрытое, а доверенной модели нет. По ней «Здоровье» считает, сколько
// работ ждёт назначения модели.
const reasonNoTrusted = "доверенной модели нет, наружу не отдаю"

// homeModel — имя домашней модели или пусто. Отдельно от
// `localModelOrNone`, который отвечает словом «нет» для выпадающего списка
// ролей: здесь пустая строка означает «домашней нет», и это законное
// состояние — после переезда на маленький сервер локальных моделей может не
// быть вовсе.
func (e *Engine) homeModel() string {
	if n := e.localModelOrNone(); n != roleNoneName {
		return n
	}
	return ""
}

// raiseClass — поднять класс сущности по новым сведениям. Класс умеет
// только расти: разбор может открыть, что в файле паспорт, но не может
// доказать, что его там нет. Понижение — дело человека, и его пометка
// (`by` непустой) не трогается вовсе.
func (e *Engine) raiseClass(kind string, id int64, class, why string) {
	if id == 0 || class == "" || class == store.ClassPlain {
		return
	}
	if m, ok := e.store.ClassMarkOf(kind, id); ok {
		if m.By != "" {
			return // пометка человека главнее любых примет
		}
		if weight(m.Class) >= weight(class) {
			return
		}
	}
	_ = e.store.SetContentClass(kind, id, class, "", why)
}

func weight(class string) int {
	switch class {
	case store.ClassSecret:
		return 3
	case store.ClassSensitive:
		return 2
	case store.ClassPlain:
		return 1
	}
	return 0
}

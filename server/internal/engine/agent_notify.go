package engine

// Уведомления на маке показывает житель машины, а не клиент: приложение
// снято, а провод к агенту шифрован, опознан сертификатом и переживает
// выкаты. Сёма зовёт способность «уведомления» и считает запись
// доставленной только после ответа «показал» — иначе «доставлено»
// означало бы «отправили в пустоту».
//
// Кому и когда показывать, решает по-прежнему Сёма: порог, источники и
// тихие часы канала `mac` считаются здесь, житель машины показывает то,
// что дали.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// CanNotify — способность показывать уведомления человеку. Имя то же, что
// человек видит на экране «Агенты».
const CanNotify = "уведомления"

const (
	notifyShowMethod  = CanNotify + ".показать"
	notifyCountMethod = CanNotify + ".счётчик"
	notifyAskTimeout  = 30 * time.Second
)

// NotifyCard — что уходит жителю машины. Состав тот же, что показывал
// клиент: заголовок, итог, уровень и экран для перехода.
type NotifyCard struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Text   string `json:"text"`
	Level  string `json:"level"`
	Screen string `json:"screen"`
	At     string `json:"at,omitempty"`
}

// notifyShown — ответ агента: показал или нет и почему.
type notifyShown struct {
	Shown bool   `json:"показал"`
	Why   string `json:"почему,omitempty"`
}

// notifyCard — запись в том виде, в каком её показывают человеку.
func notifyCard(n *store.Notification) NotifyCard {
	return NotifyCard{ID: n.ID, Title: n.Title, Text: n.Summary, Level: n.Level,
		Screen: notifyScreen(n), At: n.Created}
}

// notifyScreen — куда вести по нажатию: на саму запись, если экран у неё
// есть, иначе на список.
func notifyScreen(n *store.Notification) string {
	for _, a := range n.Actions {
		if a.Kind == "goto" && a.Ref != "" {
			return a.Ref
		}
	}
	return "notifications"
}

// notifyAgents — жители машин, которым разрешено показывать и которые
// сейчас на связи. Порядок постоянный: на двух машинах человек не должен
// гадать, где выскочит запись.
func (e *Engine) notifyAgents() []string {
	list, err := e.store.Agents()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range list {
		if e.AgentAllowed(a.Name, CanNotify) && e.AgentOnline(a.Name) {
			out = append(out, a.Name)
		}
	}
	sort.Strings(out)
	return out
}

// AgentOnline — есть ли сейчас провод с этим агентом.
func (e *Engine) AgentOnline(name string) bool {
	e.linkMu.Lock()
	defer e.linkMu.Unlock()
	return e.links[name] != nil
}

// deliverMac — показать запись на маке. Не показали — не пометили
// доставленной: пропущенное житель машины доберёт сам при подключении.
func (e *Engine) deliverMac(n *store.Notification) {
	agents := e.notifyAgents()
	if len(agents) == 0 {
		e.noteMacFail("некому показать: житель машины не на связи или способность «уведомления» не разрешена")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyAskTimeout)
	defer cancel()
	shown, why := false, ""
	for _, name := range agents {
		raw, err := e.AskAgent(ctx, name, CanNotify, notifyShowMethod, notifyCard(n))
		if err != nil {
			why = err.Error()
			continue
		}
		var res notifyShown
		if err := json.Unmarshal(raw, &res); err != nil {
			why = "непонятный ответ агента: " + err.Error()
			continue
		}
		if !res.Shown {
			why = res.Why
			continue
		}
		shown = true
	}
	if !shown {
		if why == "" {
			why = "показать не удалось"
		}
		e.noteMacFail(why)
		return
	}
	e.store.MarkDelivered(n.ID, "mac", "")
	e.noteMacFail("")
}

// noteMacFail — последняя причина, по которой запись не показали. Видна в
// строке канала на «Настройки → Уведомления»: молчащий канал должен
// объяснять себя.
func (e *Engine) noteMacFail(why string) {
	if why == "" {
		_ = e.store.SetSetting("notify.mac_last_err", "")
		return
	}
	_ = e.store.SetSetting("notify.mac_last_err", time.Now().Format("02.01 15:04")+" · "+clip(why, 200))
	log.Printf("уведомления: на маке не показали: %s", why)
}

// MacChannelNote — кто доставляет канал «Mac» и что было последним отказом.
func (e *Engine) MacChannelNote() string {
	agents := e.notifyAgents()
	if len(agents) == 0 {
		var names []string
		if list, err := e.store.Agents(); err == nil {
			for _, a := range list {
				if a.State == store.AgentOK {
					names = append(names, a.Name)
				}
			}
		}
		if len(names) == 0 {
			return "показывать некому: житель машины не подключён"
		}
		return "показывать некому: у агентов " + strings.Join(names, ", ") +
			" не разрешена способность «уведомления» или они не на связи"
	}
	note := "показывает житель машины: " + strings.Join(agents, ", ")
	if last := e.store.Setting("notify.mac_last_err", ""); last != "" {
		note += " · последний отказ: " + last
	}
	return note
}

// NotifyFeedFor — что житель машины пропустил, пока был без связи. Тот же
// отбор, что у доставки: порог канала, источники, тихие часы.
func (e *Engine) NotifyFeedFor(after int64, limit int) []NotifyCard {
	mac := e.macChannel()
	if mac == nil || inQuietHours(mac, time.Now()) {
		return nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	list, _ := e.store.NotificationsSince(after, mac.MinLevel, limit)
	var out []NotifyCard
	for _, n := range list {
		if mac.Sources != "" && !hasString(strings.Split(strings.ReplaceAll(mac.Sources, " ", ""), ","), NotifySourceGroup(n)) {
			continue
		}
		out = append(out, notifyCard(n))
		e.store.MarkDelivered(n.ID, "mac", "")
	}
	return out
}

// macChannel — канал «Mac», если он включён.
func (e *Engine) macChannel() *store.NotifyChannel {
	chans, _ := e.store.NotifyChannels()
	for _, c := range chans {
		if c.Kind == "mac" && c.Enabled {
			return c
		}
	}
	return nil
}

// ---- счётчик непрочитанных ----

// notifyCountWait — окно склейки для счётчика: человек разбирает записи
// пачкой, и слать число на каждую — значит будить провод впустую.
const notifyCountWait = time.Second

var notifyCountMu sync.Mutex
var notifyCountPending bool

// PushNotifyCount — сказать жителям машин, сколько записей ждёт человека.
// Зовётся, когда счётчик мог измениться; частые правки склеиваются в одну
// посылку.
func (e *Engine) PushNotifyCount() {
	notifyCountMu.Lock()
	if notifyCountPending {
		notifyCountMu.Unlock()
		return
	}
	notifyCountPending = true
	notifyCountMu.Unlock()
	go func() {
		time.Sleep(notifyCountWait)
		notifyCountMu.Lock()
		notifyCountPending = false
		notifyCountMu.Unlock()
		e.sendNotifyCount()
	}()
}

func (e *Engine) sendNotifyCount() {
	agents := e.notifyAgents()
	if len(agents) == 0 {
		return
	}
	n := e.NotifyCounter()
	ctx, cancel := context.WithTimeout(context.Background(), notifyAskTimeout)
	defer cancel()
	for _, name := range agents {
		if _, err := e.AskAgent(ctx, name, CanNotify, notifyCountMethod,
			map[string]any{"ждут": n}); err != nil {
			log.Printf("уведомления: счётчик не ушёл агенту «%s»: %v", name, err)
		}
	}
}

// notifyAsk — что Сёма отвечает жителю машины про уведомления.
func (e *Engine) notifyAsk(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "лента":
		var p struct {
			After int64 `json:"после"`
			Limit int   `json:"сколько"`
		}
		if len(params) > 0 {
			_ = json.Unmarshal(params, &p)
		}
		return map[string]any{
			"записи":    e.NotifyFeedFor(p.After, p.Limit),
			"последний": e.store.LastNotificationID(),
			"ждут":      e.NotifyCounter(),
			"адрес":     e.WebAddress(),
		}, nil
	case "счётчик":
		return map[string]any{"ждут": e.NotifyCounter(), "адрес": e.WebAddress()}, nil
	}
	return nil, fmt.Errorf("Сёма не отвечает на «%s.%s»", CanNotify, method)
}

// WebAddress — адрес, по которому человек открывает Сёму в браузере.
// Нужен жителю машины: по нажатию он открывает экран записи.
func (e *Engine) WebAddress() string {
	if v := strings.TrimSpace(e.store.SettingText(store.AuthOrigin)); v != "" {
		return v
	}
	return ""
}

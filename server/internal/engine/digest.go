package engine

// Дайджест «Почты»: письма не-спам за окно, разложенные по группам
// справочника. Окно — настройка: сегодня, с прошлого захода, неделя.
// «С прошлого захода» считается от прошлого открытия «Почты», но не меньше
// суток, чтобы короткие заходы не обнуляли сводку.

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	digestWindowKey = "digest.window" // today | since | week
	digestSeenKey   = "digest.seen"   // когда «Почту» открывали в прошлый раз
	digestSinceKey  = "digest.since"  // начало окна «с прошлого захода»
	digestVisitGap  = 30 * time.Minute
)

var DigestWindows = []struct{ ID, Title string }{
	{"today", "сегодня"}, {"since", "с прошлого захода"}, {"week", "неделя"},
}

func (e *Engine) DigestWindow() string {
	w := e.store.Setting(digestWindowKey, "since")
	for _, o := range DigestWindows {
		if o.ID == w {
			return w
		}
	}
	return "since"
}

func (e *Engine) SetDigestWindow(w string) error {
	for _, o := range DigestWindows {
		if o.ID == w {
			return e.store.SetSetting(digestWindowKey, w)
		}
	}
	return nil
}

// digestSince — начало окна, с отметкой захода. Новый заход — если «Почту»
// не открывали дольше получаса: тогда прошлое открытие становится началом.
func (e *Engine) digestSince(window string) (time.Time, string) {
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch window {
	case "today":
		return day, "сегодня"
	case "week":
		return day.AddDate(0, 0, -6), "за неделю"
	}
	seen, _ := time.Parse(time.RFC3339, e.store.Setting(digestSeenKey, ""))
	since, _ := time.Parse(time.RFC3339, e.store.Setting(digestSinceKey, ""))
	if seen.IsZero() || now.Sub(seen) > digestVisitGap {
		since = seen
		if since.IsZero() || now.Sub(since) < 24*time.Hour {
			since = now.Add(-24 * time.Hour)
		}
		_ = e.store.SetSetting(digestSinceKey, since.Format(time.RFC3339))
	}
	_ = e.store.SetSetting(digestSeenKey, now.Format(time.RFC3339))
	if since.IsZero() {
		since = now.Add(-24 * time.Hour)
	}
	label := "с " + humanSince(since, now)
	return since, label
}

func humanSince(t, now time.Time) string {
	days := []string{"воскресенья", "понедельника", "вторника", "среды", "четверга", "пятницы", "субботы"}
	months := []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	if now.Sub(t) < 36*time.Hour {
		return "вчера " + t.Format("15:04")
	}
	return days[int(t.Weekday())] + " " + t.Format("2") + " " + months[int(t.Month())-1]
}

// DigestItem — одна строка сводки: письмо и то, что Сёма про него понял.
type DigestItem struct {
	Msg     *store.Message
	Receipt *ReceiptInfo // чек письма; nil — чека нет
	Files   []*store.Attachment
}

// ReceiptInfo — что Сёма знает о чеке письма. Отдельной таблицы чеков нет:
// получатель, сумма и итог лежат у самого файла (вид «чек»).
type ReceiptInfo struct {
	Amount   float64
	Currency string
	Vendor   string
	Summary  string
}

// receiptsFor — чеки писем: файлы с видом «чек», привязанные к этим письмам.
func (e *Engine) receiptsFor(ids []int64) map[int64]*ReceiptInfo {
	out := map[int64]*ReceiptInfo{}
	if len(ids) == 0 {
		return out
	}
	found, err := e.store.ReceiptsForMessages(ids)
	if err != nil {
		log.Printf("чеки писем: %v", err)
		return out
	}
	for id, r := range found {
		out[id] = &ReceiptInfo{Amount: r.Amount, Currency: r.Currency, Vendor: r.Vendor, Summary: r.Summary}
	}
	return out
}

type DigestSection struct {
	Group   *store.DigestGroup
	Items   []DigestItem
	Sum     map[string]float64 // сумма чеков по валютам
	Senders []SenderCount      // для свёрнутой группы: кто и сколько
}

type SenderCount struct {
	Name  string
	Count int
}

type Digest struct {
	Window   string
	Since    time.Time
	Label    string
	Total    int
	Notable  int // не уведомления
	Notified int
	Spam     int
	Sections []DigestSection
}

// digestStatuses — что считается «не спамом» для сводки.
var digestStatuses = []string{store.StatusInboxOK, store.StatusHam, store.StatusArchived}

// Digest собирает сводку ящика (или всех ящиков) за текущее окно. о — отсев
// сборки: чего в ней нет вовсе (снимок на устройстве — закрытые для облака
// ящики и «секретное» не должны долежать до диска ни в одном виде); nil —
// обычный показ человеку, никого не убираем.
func (e *Engine) Digest(account string, o *store.Exclusion) (*Digest, error) {
	window := e.DigestWindow()
	since, label := e.digestSince(window)
	now := time.Now()
	msgs, err := e.store.MessagesBetween(account, o, since, now.Add(time.Hour), digestStatuses...)
	if err != nil {
		return nil, err
	}
	groups, err := e.store.DigestGroups()
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	files, _ := e.store.AttachmentsForMessages(ids)
	// письма уже отсеяны, а вложение секретно само по себе: паспорт приезжает
	// файлом к письму, чья тема ни о чём не говорит
	for id, list := range files {
		files[id] = e.store.FilterExcludedAttachments(list, o)
	}
	receipts := e.receiptsFor(ids)
	d := &Digest{Window: window, Since: since, Label: label, Total: len(msgs)}
	d.Spam = e.store.CountBetween(account, o, since, now.Add(time.Hour), store.StatusAutoMoved, store.StatusConfirmed, store.StatusQueued)
	byGroup := map[string]*DigestSection{}
	for _, g := range groups {
		sec := &DigestSection{Group: g, Sum: map[string]float64{}}
		byGroup[g.Name] = sec
		d.Sections = append(d.Sections, *sec)
	}
	secIndex := map[string]int{}
	for i, sec := range d.Sections {
		secIndex[sec.Group.Name] = i
	}
	for _, m := range msgs {
		if m.Direction == "out" {
			d.Total--
			continue // наши исходящие: в архиве и поиске есть, в сводке «что пришло» — нет
		}
		item := DigestItem{Msg: m, Receipt: receipts[m.ID], Files: files[m.ID]}
		g := digestGroupOf(groups, m, item.Receipt != nil, item.Files)
		if g == nil {
			continue
		}
		i := secIndex[g.Name]
		d.Sections[i].Items = append(d.Sections[i].Items, item)
		if item.Receipt != nil && item.Receipt.Amount > 0 {
			d.Sections[i].Sum[item.Receipt.Currency] += item.Receipt.Amount
		}
		if g.Collapsed {
			d.Notified++
		} else {
			d.Notable++
		}
	}
	for i := range d.Sections {
		sec := &d.Sections[i]
		if !sec.Group.Collapsed {
			continue
		}
		counts := map[string]int{}
		for _, it := range sec.Items {
			counts[senderLabel(it.Msg)]++
		}
		for name, n := range counts {
			sec.Senders = append(sec.Senders, SenderCount{name, n})
		}
		sort.Slice(sec.Senders, func(a, b int) bool {
			if sec.Senders[a].Count != sec.Senders[b].Count {
				return sec.Senders[a].Count > sec.Senders[b].Count
			}
			return sec.Senders[a].Name < sec.Senders[b].Name
		})
	}
	return d, nil
}

// DigestGroupOf — имя группы для одного письма: так модули говорят с ядром
// на одном языке («правило для группы «счета и чеки»»).
func (e *Engine) DigestGroupOf(m *store.Message) string {
	groups, err := e.store.DigestGroups()
	if err != nil {
		return ""
	}
	atts, _ := e.store.AttachmentsOfMessage(m.ID)
	hasReceipt := len(e.receiptsFor([]int64{m.ID})) > 0
	if g := digestGroupOf(groups, m, hasReceipt, atts); g != nil {
		return g.Name
	}
	return ""
}

// digestGroupOf — первая группа по порядку, у которой сошлась примета.
// «От людей» — то, что не от робота и не сошлось раньше, поэтому группы с
// приметой People проверяются после остальных.
func digestGroupOf(groups []*store.DigestGroup, m *store.Message, hasReceipt bool, atts []*store.Attachment) *store.DigestGroup {
	addr := strings.ToLower(m.FromAddr)
	subject := strings.ToLower(m.Subject)
	robot := false
	for _, g := range groups {
		if g.Rules.Robots && matchAny(addr, g.Rules.Senders) {
			robot = true
		}
	}
	var people *store.DigestGroup
	for _, g := range groups {
		r := g.Rules
		if r.People {
			if people == nil {
				people = g
			}
			continue
		}
		switch {
		case r.Receipt && hasReceipt:
			return g
		case len(r.Senders) > 0 && !r.Robots && matchAny(addr, r.Senders):
			return g
		case len(r.Words) > 0 && matchAny(subject, r.Words):
			return g
		case len(r.Attachments) > 0 && hasExt(atts, r.Attachments):
			return g
		case r.Robots && robot:
			return g
		}
	}
	if !robot && people != nil {
		return people
	}
	// робот без своей группы — в первую свёрнутую, иначе никуда
	for _, g := range groups {
		if g.Rules.Robots {
			return g
		}
	}
	return people
}

func matchAny(s string, words []string) bool {
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" && strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func hasExt(atts []*store.Attachment, exts []string) bool {
	for _, a := range atts {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(a.Filename)), ".")
		for _, x := range exts {
			if ext == strings.ToLower(strings.TrimPrefix(strings.TrimSpace(x), ".")) {
				return true
			}
		}
	}
	return false
}

// senderLabel — имя отправителя для свёрнутой строки: «GitHub 18».
func senderLabel(m *store.Message) string {
	if n := strings.TrimSpace(m.FromName); n != "" {
		return n
	}
	addr := m.FromAddr
	if at := strings.Index(addr, "@"); at >= 0 {
		return addr[at+1:]
	}
	return addr
}

// WorkSummary — что Сёма сделал за окно, одной строкой.
func (e *Engine) WorkSummary(account string, since time.Time) string {
	now := time.Now().Add(time.Hour)
	var parts []string
	if n := e.store.CountBetween(account, nil, since, now, store.StatusArchived); n > 0 {
		parts = append(parts, "разобрано из папок "+plural(n, "письмо", "письма", "писем"))
	}
	if n := e.store.CountBetween(account, nil, since, now, store.StatusInboxOK, store.StatusHam); n > 0 {
		parts = append(parts, "проверено "+plural(n, "письмо", "письма", "писем"))
	}
	if n := e.store.CountBetween(account, nil, since, now, store.StatusAutoMoved, store.StatusConfirmed); n > 0 {
		parts = append(parts, "в спам "+plural(n, "письмо", "письма", "писем"))
	}
	kinds := e.store.CountMemoriesSince(since)
	if n := kinds[store.MemFact] + kinds[store.MemLesson] + kinds[store.MemCrystal]; n > 0 {
		parts = append(parts, "память: "+plural(kinds[store.MemFact], "факт", "факта", "фактов")+
			", "+plural(kinds[store.MemLesson], "урок", "урока", "уроков"))
	}
	if len(parts) == 0 {
		return "за окно ничего нового"
	}
	return strings.Join(parts, " · ")
}

// plural — «1 письмо, 2 письма, 5 писем».
func plural(n int, one, few, many string) string {
	form := many
	switch {
	case n%10 == 1 && n%100 != 11:
		form = one
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20):
		form = few
	}
	return strconv.Itoa(n) + " " + form
}

// DigestCollapseFrom — со скольких писем группу имеет смысл сворачивать.
//
// Свёртка бережёт место, а короткую группу беречь не от чего: «Счета и
// чеки · 1 · свёрнуто» со строкой «Ozon 1» вместо самого письма — это
// потеря, а не экономия. Порог — настройка: у одного человека
// «Уведомления» это три письма в день, у другого сорок.
func (e *Engine) DigestCollapseFrom() int {
	v, err := strconv.Atoi(e.store.Setting("digest.collapse_from", "4"))
	if err != nil || v < 1 {
		return 4
	}
	return v
}

func (e *Engine) SetDigestCollapseFrom(n int) error {
	if n < 1 {
		return fmt.Errorf("сворачивать меньше чем от одного письма нельзя")
	}
	return e.store.SetSetting("digest.collapse_from", strconv.Itoa(n))
}

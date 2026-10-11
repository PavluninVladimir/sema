package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Уведомления — всё, что ждёт человека, одной формой. Ядро кладёт сюда
// запросы (команды, права, скиллы, отпечатки, бюджет), почтовые правила —
// письма со сроком, модули — свои отчёты через sema.notify. Каналы
// доставляют наружу: Mac (лента для клиента), модули-каналы.

const (
	WorkerNotifyDue    = "notify_due"
	notifyKeepDaysKey  = "notify.keep_days"
	notifyCounterLevel = "notify.counter_level"
	notifySnoozeKey    = "notify.snooze"
	phoneTabsKey       = "menu.tabs"
)

// phoneTabsDefault — что стоит на нижней полосе телефона, кроме «Меню».
// Три места, куда ходят чаще всего; у кого-то это «Задачи» и «Файлы»,
// поэтому список, а не код.
const phoneTabsDefault = "notifications,inbox,ask"

// PhoneTabs — разделы нижней полосы по порядку.
func (e *Engine) PhoneTabs() []string {
	var out []string
	for _, part := range strings.Split(e.store.Setting(phoneTabsKey, phoneTabsDefault), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// notifySnoozeDefault — на сколько предлагается отложить запись. Список,
// а не код: у кого-то «до вечера» это шесть, у кого-то девять, и слова
// разбирает тот же разборщик сроков, что и напоминания из чата.
const notifySnoozeDefault = "через час, сегодня вечером, завтра, через неделю"

// SnoozeOptions — на сколько можно отложить: подписи для кнопок.
func (e *Engine) SnoozeOptions() []string {
	var out []string
	for _, part := range strings.Split(e.store.Setting(notifySnoozeKey, notifySnoozeDefault), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		out = []string{"завтра"}
	}
	return out
}

// SnoozeNotify — отложить запись до срока, названного словами. Срок в
// прошлом — отказ: молча отложить на вчера значит поднять запись сразу,
// и человек решит, что кнопка не работает.
func (e *Engine) SnoozeNotify(id int64, spec string) (time.Time, error) {
	when := script.ParseDate(spec)
	if when.IsZero() || !when.After(time.Now()) {
		return time.Time{}, fmt.Errorf("не понял, до каких пор откладывать: %q", spec)
	}
	if err := e.store.SnoozeNotification(id, when.Format(time.RFC3339)); err != nil {
		return time.Time{}, err
	}
	return when, nil
}

// levelWithDue — «у записи со сроком уровень не ниже warn». Напоминание
// «пополнить баланс до пятницы» — не «к сведению»: при пороге каналов
// «внимание» такая запись не уходила никуда, и о просрочке человек узнавал
// от провайдера, а не от Сёмы. Считаем один раз в общей двери, а не в
// каждом месте, где рождается запись.
//
// Уровень «ok» не трогаем: это не срок человека, а наблюдение Сёмы («жду,
// ответят ли») — оно живёт в сводке и поднимается задачей «Сроки», когда
// срок наступит. Поднять его сразу значило бы слать в телефон каждое
// письмо от человека.
func levelWithDue(level, due string) string {
	if strings.TrimSpace(due) == "" || level == "ok" {
		return level
	}
	if store.LevelRank(level) < store.LevelRank("warn") {
		return "warn"
	}
	return level
}

// Notify кладёт уведомление и рассылает по каналам.
func (e *Engine) Notify(n *store.Notification) (int64, error) {
	if n.Source == "" {
		n.Source = "core"
	}
	n.Level = levelWithDue(n.Level, n.Due)
	id, err := e.store.PutNotification(n)
	if err != nil {
		return 0, err
	}
	e.deliver(n)
	return id, nil
}

// CloseNotification — решение пришло из старого места: закрываем по ключу.
func (e *Engine) CloseNotification(key, state, by string) {
	e.store.CloseNotificationByKey(key, state, by)
}

// NotifyCounter — сколько записей ждёт человека. Считаем всё открытое
// кроме «в норме»: раньше порог отсекал часть записей из счётчика, и в
// меню стояло «2», когда экран показывал больше — человек считал, что
// остальные потерялись. Порогом управляем цветом, а не наличием числа.
func (e *Engine) NotifyCounter() int {
	return e.store.CountOpenNotifications("info")
}

func (e *Engine) NotifyKeepDays() int {
	v, _ := strconv.Atoi(e.store.Setting(notifyKeepDaysKey, "90"))
	if v <= 0 {
		v = 90
	}
	return v
}

// NotifySourceGroup — чип фильтра для источника записи.
func NotifySourceGroup(n *store.Notification) string {
	switch {
	case n.Source == "mail":
		return "mail"
	case strings.HasPrefix(n.Key, "server:"):
		return "servers" // отчёты по серверам; команды и отпечатки — запросы
	case n.Source == "core":
		return "requests"
	case strings.HasPrefix(n.Key, "reminder:"):
		return "mail"
	}
	return "modules"
}

// ---- решения ----

// NotifyDecide исполняет действие номер idx у записи id и закрывает её.
func (e *Engine) NotifyDecide(ctx context.Context, id int64, idx int, by string) error {
	n, ok := e.store.Notification(id)
	if !ok {
		return fmt.Errorf("уведомления %d нет", id)
	}
	if idx < 0 || idx >= len(n.Actions) {
		return fmt.Errorf("у уведомления нет действия %d", idx)
	}
	a := n.Actions[idx]
	switch a.Kind {
	case "done":
		return e.store.SetNotificationState(id, store.NotifyDone, by)
	case "dismiss":
		return e.store.SetNotificationState(id, store.NotifyDismissed, by)
	case "goto":
		return nil // переход делает клиент
	case "ssh":
		// без чата: результат ложится в само уведомление, а разговор
		// заводится кнопкой «Спросить Сёму», если он понадобится
		chatID := e.notifyChatID(n.ID)
		o := SSHOrigin{Who: "уведомление «" + clip(n.Title, 40) + "» · " + by, ChatID: chatID}
		out := e.RequestSSH(ctx, o, a.Host, a.Cmd)
		if out.Err != "" {
			return fmt.Errorf("%s", out.Err)
		}
		if out.PendingID > 0 {
			// команда изменяющая: подтверждаем сразу — кнопку нажал человек
			if err := e.ApproveSSH(ctx, out.PendingID, false); err != nil {
				return err
			}
		} else {
			card := ToolCard{Title: a.Host + " · " + a.Cmd, Code: out.Output, Seconds: out.Duration.Seconds(),
				SSH: &SSHCard{Host: a.Host, Cmd: a.Cmd, Status: "executed", Code: out.Code}}
			_ = e.saveToolCard(chatID, card)
			e.continueChat(chatID)
		}
		return e.recordSSHResult(n, a.Host, a.Cmd, by, chatID)
	case "module":
		params := map[string]string{}
		if a.Arg != "" {
			params["arg"] = a.Arg
		}
		if err := e.ModuleAction(ctx, a.Module, a.Fn, params); err != nil {
			return err
		}
		return e.store.SetNotificationState(id, store.NotifyDone, by)
	case "core":
		return e.notifyCoreOp(ctx, n, a, by)
	}
	return fmt.Errorf("неизвестное действие %q", a.Kind)
}

func (e *Engine) notifyCoreOp(ctx context.Context, n *store.Notification, a store.NotifyAction, by string) error {
	switch a.Op {
	// ответ человека про картинку, в которой не нашлось текста. Вопрос без
	// кнопок вопросом не был: закрыв запись, человек ничего не решал, и
	// следующий проход спрашивал снова
	case "role_use_spare":
		// «Поставить запасную» из записи про отказ модели: ждать
		// отказавшую бессмысленно, а искать роль среди полусотни моделей
		// — работа, которой быть не должно
		if err := e.MakeSpareMain(a.Ref); err != nil {
			return err
		}
		e.CloseNotification("role:"+a.Ref, store.NotifyDone, "роли назначена другая модель")
		return nil
	case "vision_show":
		return e.DecideByImage(a.Ref, "показать")
	case "vision_skip":
		return e.DecideByImage(a.Ref, "пропустить")
	case "approve_ssh":
		pid, _ := strconv.ParseInt(a.Ref, 10, 64)
		chatID := int64(0)
		if p, err := e.store.SSHPendingByID(pid); err == nil {
			chatID = p.ChatID
		}
		if err := e.ApproveSSH(ctx, pid, false); err != nil {
			return err
		}
		if p, err := e.store.SSHPendingByID(pid); err == nil {
			return e.recordSSHResult(n, p.Host, p.Cmd, by, chatID)
		}
	case "reject_ssh":
		pid, _ := strconv.ParseInt(a.Ref, 10, 64)
		if err := e.RejectSSH(pid); err != nil {
			return err
		}
	case "rule_ask", "rule_allow", "rule_dismiss":
		// команда без правила: человек завёл правило «Спросить» или
		// «Разрешить» либо оставил её запрещённой. Запись закрывает само
		// решение, а итог выполненной команды ложится в неё же
		id, _ := strconv.ParseInt(a.Ref, 10, 64)
		answer := map[string]string{"rule_ask": store.NoRuleAsk, "rule_allow": store.NoRuleAllow, "rule_dismiss": store.NoRuleDismissed}[a.Op]
		return e.DecideNoRule(ctx, id, answer, by)
	case "accept_price":
		// цена приходит вместе с именем: «модель|цена». Отдельного места
		// хранить предложение не заводим — оно и так лежит в уведомлении
		name, price, ok := strings.Cut(a.Ref, "|")
		if !ok {
			return fmt.Errorf("предложение цены испорчено")
		}
		if err := e.SetModelPrice(name, price); err != nil {
			return err
		}
		log.Printf("цена модели %q принята человеком: %s", name, price)
		// принято — карточка закрывается: иначе цена меняется, а кнопка
		// выглядит нерабочей
		return e.store.SetNotificationState(n.ID, store.NotifyDone, by)
	case "accept_prices":
		// Ref — провайдер, Arg — что принять: new, all или force. Само
		// предложение лежит в настройках (price.proposal.<провайдер>):
		// в кнопку двадцать строк не поместятся
		applied, skipped, err := e.AcceptPrices(a.Ref, a.Arg)
		if err != nil {
			return err
		}
		log.Printf("цены %s приняты человеком: %d, пропущено %d", a.Ref, applied, skipped)
		return e.store.SetNotificationState(n.ID, store.NotifyDone, by)
	case "accept_agent_fp":
		if err := e.AcceptAgentFP(a.Ref); err != nil {
			return err
		}
		return nil
	case "revoke_agent":
		if err := e.RevokeAgent(a.Ref); err != nil {
			return err
		}
		return e.store.SetNotificationState(n.ID, store.NotifyDone, by)
	case "grant_class":
		if err := e.DecideConsent(a.Ref, a.Arg == "allow"); err != nil {
			return err
		}
		return nil
	case "approve_mcp":
		pid, _ := strconv.ParseInt(a.Ref, 10, 64)
		if _, err := e.ApproveMCP(context.Background(), pid, true); err != nil {
			return err
		}
		return nil // итог уже дописан в само уведомление
	case "approve_mcp_always":
		pid, _ := strconv.ParseInt(a.Ref, 10, 64)
		if _, err := e.ApproveMCPAlways(context.Background(), pid); err != nil {
			return err
		}
		return nil
	case "reject_mcp":
		pid, _ := strconv.ParseInt(a.Ref, 10, 64)
		if _, err := e.ApproveMCP(context.Background(), pid, false); err != nil {
			return err
		}
		return e.store.SetNotificationState(n.ID, store.NotifyDismissed, by)
	case "tg_wait_skip":
		account, id, _, ok := e.tgFromLinks(a.Ref)
		if !ok {
			return fmt.Errorf("непонятная ссылка на чат: %q", a.Ref)
		}
		return e.TGWaitSkip(account, id)
	case "tg_wait_off":
		account, id, _, ok := e.tgFromLinks(a.Ref)
		if !ok {
			return fmt.Errorf("непонятная ссылка на чат: %q", a.Ref)
		}
		return e.TGWaitOff(account, id, true)
	case "decide_skill":
		accept := a.Arg == "accept"
		if err := e.DecideSkill(a.Ref, accept); err != nil {
			return err
		}
		if !accept {
			return e.store.SetNotificationState(n.ID, store.NotifyDismissed, by)
		}
	case "accept_fp":
		if err := e.AcceptFingerprint(a.Ref); err != nil {
			return err
		}
	case "reject_fp":
		return e.store.SetNotificationState(n.ID, store.NotifyDismissed, by)
	case "reset_budget":
		// снять паузу: модуль включается человеком с «Модулей», здесь только «понял»
		return e.store.SetNotificationState(n.ID, store.NotifyDone, by)
	default:
		return fmt.Errorf("неизвестная операция %q", a.Op)
	}
	return e.store.SetNotificationState(n.ID, store.NotifyDone, by)
}

// recordSSHResult — итог команды в само уведомление: код и хвост вывода.
// Успех закрывает запись, ошибка оставляет открытой с тревогой.
func (e *Engine) recordSSHResult(n *store.Notification, host, cmd, by string, chatID int64) error {
	if chatID > 0 {
		_ = e.store.AddNotificationAction(n.ID, store.NotifyAction{Label: "Открыть чат", Kind: "goto", Screen: "chat:" + strconv.FormatInt(chatID, 10)})
	}
	entry, ok := e.store.LastSSHLogFor(host, cmd)
	if !ok {
		return e.store.SetNotificationState(n.ID, store.NotifyDone, by)
	}
	lines := strings.Split(strings.TrimSpace(entry.Output), "\n")
	if len(lines) > 12 {
		lines = append([]string{"…"}, lines[len(lines)-12:]...)
	}
	tail := strings.Join(lines, "\n")
	when := whenShort(entry.At)
	if entry.Code == 0 {
		field := store.NotifyField{Label: "Результат", Value: fmt.Sprintf("%s · код 0 · %.1f с\n%s", when, float64(entry.Millis)/1000, tail), Level: "ok"}
		return e.store.SetNotificationResult(n.ID, field, "выполнено · "+clip(cmd, 50)+" · код 0", n.Level, store.NotifyDone, by+" · выполнено")
	}
	field := store.NotifyField{Label: "Результат", Value: fmt.Sprintf("%s · код %d · %.1f с\n%s", when, entry.Code, float64(entry.Millis)/1000, tail), Level: "alert"}
	err := e.store.SetNotificationResult(n.ID, field, "команда завершилась с ошибкой · код "+strconv.Itoa(entry.Code)+" · "+clip(cmd, 50), "alert", store.NotifyOpen, "")
	if chatID > 0 {
		e.handOffFailure(n, host, cmd, entry) // разговор уже идёт — пусть Сёма разберётся там
	}
	return err
}

// NotifyChatID — чат уведомления, если он уже заведён кнопкой «Спросить Сёму».
func (e *Engine) NotifyChatID(nid int64) int64 { return e.notifyChatID(nid) }

func (e *Engine) notifyChatID(nid int64) int64 {
	id, _ := strconv.ParseInt(e.store.Setting(fmt.Sprintf("notify.chat.%d", nid), ""), 10, 64)
	if id > 0 {
		if _, err := e.store.Chat(id); err == nil {
			return id
		}
	}
	return 0
}

// NotifyChat — «Спросить Сёму»: чат заводится по кнопке, а не сам. В него
// кладётся всё, что знает уведомление, и Сёма продолжает работу там.
func (e *Engine) NotifyChat(nid int64) (int64, error) {
	n, ok := e.store.Notification(nid)
	if !ok {
		return 0, fmt.Errorf("уведомления %d нет", nid)
	}
	if id := e.notifyChatID(nid); id > 0 {
		return id, nil
	}
	// уведомление о переписке разбирается в самой переписке: заводить
	// рядом пустой чат бессмысленно — Сёма всё равно не увидит, о чём
	// говорили, и переспросит «не хватает контекста»
	if id := e.peerChatOfNotice(n.Key); id > 0 {
		_ = e.store.SetSetting(fmt.Sprintf("notify.chat.%d", nid), strconv.FormatInt(id, 10))
		_ = e.store.SetSetting(fmt.Sprintf("chat.notify.%d", id), strconv.FormatInt(nid, 10))
		note := "Разбираемся с уведомлением «" + n.Title + "»: " + n.Summary +
			"\n\nПереписка выше — она же контекст. Продолжай сам: предложи ответ блоком " +
			"sema-tg:<кому>, поставь ожидание блоком sema-timer:<срок> или закрой вопрос блоком sema-done:<итог>."
		if err := e.store.AddChatMsg(e.chatForOutput(id), "note", note); err != nil {
			return 0, err
		}
		e.continueChat(id)
		return id, nil
	}
	id, err := e.store.CreateChat("Уведомление · " + clip(n.Title, 40))
	if err != nil {
		return 0, err
	}
	_ = e.store.SetSetting(fmt.Sprintf("notify.chat.%d", nid), strconv.FormatInt(id, 10))
	_ = e.store.SetSetting(fmt.Sprintf("chat.notify.%d", id), strconv.FormatInt(nid, 10))
	e.touchChatExpiry(id)
	var b strings.Builder
	b.WriteString("Разбираемся с уведомлением «" + n.Title + "».\n")
	if n.Summary != "" {
		b.WriteString(n.Summary + "\n")
	}
	for _, f := range n.Fields {
		if f.Value != "" {
			b.WriteString("\n" + f.Label + ": " + f.Value)
		}
	}
	for _, a := range n.Actions {
		if a.Kind == "ssh" {
			b.WriteString("\n\nПредложенная команда на " + a.Host + ": " + a.Cmd + " (выполнится только после подтверждения).")
		}
	}
	b.WriteString("\n\nПродолжай сам: следующий шаг — команда блоком sema-ssh:<сервер> или ожидание блоком sema-timer:<срок>. " +
		"Закончишь — ответь блоком sema-done:<итог>.")
	// служебная вводная, а не ваша реплика: роль «note» рисуется отдельно,
	// иначе выглядит так, будто это писали вы
	if err := e.store.AddChatMsg(e.chatForOutput(id), "note", b.String()); err != nil {
		return id, err
	}
	e.continueChat(id)
	return id, nil
}

// commandTTLKey — сколько часов живёт чат команды; правится на «Работе Сёмы».
const commandTTLKey = "chats.command_ttl_h"

// CommandChatTTL — сколько часов живёт чат команды (chats.command_ttl_h).
// Ноль и мусор — «не задан», то есть умолчание реестра.
func (e *Engine) CommandChatTTL() int { return e.store.SettingInt(commandTTLDef) }

// commandChat — чат для команд одного уведомления: создаётся при первом
// «Выполнить», помнится в настройке, живёт до срока и убирается «Сроками».
func (e *Engine) commandChat(n *store.Notification, host string) int64 {
	key := fmt.Sprintf("notify.chat.%d", n.ID)
	if v := e.store.Setting(key, ""); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			if _, err := e.store.Chat(id); err == nil {
				return id
			}
		}
	}
	title := "Команда · " + host + " · " + time.Now().Format("02.01 15:04")
	id, err := e.store.CreateChat(title)
	if err != nil {
		return 0
	}
	_ = e.store.SetSetting(key, strconv.FormatInt(id, 10))
	_ = e.store.SetSetting(fmt.Sprintf("chat.notify.%d", id), strconv.FormatInt(n.ID, 10))
	e.touchChatExpiry(id)
	_ = e.store.AddChatMsg(e.chatForOutput(id), "assistant", "Команда из уведомления «"+clip(n.Title, 80)+"». Я выполню её, покажу результат и, если что-то не так, предложу следующий шаг. Чат живёт "+strconv.Itoa(e.CommandChatTTL())+" ч.")
	return id
}

func (e *Engine) touchChatExpiry(chatID int64) {
	expires := time.Now().Add(time.Duration(e.CommandChatTTL()) * time.Hour).Format(time.RFC3339)
	_ = e.store.SetSetting(fmt.Sprintf("chat.expires.%d", chatID), expires)
}

// purgeExpiredChats — чаты команд с истёкшим сроком удаляются.
func (e *Engine) purgeExpiredChats(now time.Time) int {
	removed := 0
	for key, val := range e.store.SettingsByPrefix("chat.expires.") {
		t, err := time.Parse(time.RFC3339, val)
		if err != nil || t.After(now) {
			continue
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(key, "chat.expires."), 10, 64)
		if id > 0 {
			// чата уже нет — осталась только отметка о сроке; убираем её
			// молча, иначе она живёт вечно и каждый час пишет в журнал
			if _, err := e.store.Chat(id); err != nil {
				e.store.DeleteSettingKey(key)
				continue
			}
			// чат уходит только после того, как его сессия закрыта сводкой в память:
			// кристалл сделает задача «Память», сам разговор больше не нужен.
			// Не удалось записать сводку — чат остаётся: терять разговор нельзя
			if !e.endChatSession(id) {
				// в пустом чате терять нечего: сводке взяться неоткуда,
				// и без этого он не удалится никогда
				if msgs, err := e.store.ChatMsgs(id, 24); err == nil && !hasTalk(msgs) {
					_ = e.store.DeleteChat(id)
					removed++
				} else {
					log.Printf("чат %d: сводка в память не записалась — чат не удаляю", id)
					continue
				}
			} else {
				_ = e.store.DeleteChat(id)
				removed++
			}
		}
		e.store.DeleteSettingKey(key)
	}
	return removed
}

// handOffFailure — ошибку разбирает Сёма, а не человек: вывод уходит в чат
// «Серверы», модель отвечает командой (sema-ssh), правкой скилла
// (sema-skill) или модуля (sema-module) — всё через подтверждение.
func (e *Engine) handOffFailure(n *store.Notification, host, cmd string, entry *store.SSHLogEntry) {
	chatID := e.notifyChatID(n.ID)
	if chatID == 0 {
		return // чата нет: ошибка видна в уведомлении, разговор заводит человек
	}
	text := fmt.Sprintf("Команда из уведомления «%s» на сервере %s завершилась с кодом %d:\n```\n%s\n```\n%s\n"+
		"Разберись, что не так, и предложи исправление: новую команду блоком sema-ssh:%s, а если ошибка в правилах или в модуле — правку скилла блоком sema-skill или обновление модуля.",
		clip(n.Title, 80), host, entry.Code, clip(cmd, 300), clip(strings.TrimSpace(entry.Output), 2500), host)
	if err := e.SendMessage(chatID, text); err != nil {
		log.Printf("уведомления: не удалось передать ошибку Сёме: %v", err)
	}
}

// chatByTitle — чат по названию; нет — создаётся.
func (e *Engine) chatByTitle(title string) int64 {
	chats, err := e.store.Chats()
	if err != nil {
		return 0
	}
	for _, c := range chats {
		if c.Title == title {
			return c.ID
		}
	}
	id, err := e.store.CreateChat(title)
	if err != nil {
		return 0
	}
	return id
}

func whenShort(rfc string) string {
	if t, err := time.Parse(time.RFC3339, rfc); err == nil {
		return t.Format("02.01 15:04")
	}
	return rfc
}

// ---- источники ядра ----

// sshNotifyKey — одна и та же команда на том же сервере заменяет прежний
// запрос, а не плодит новые (задача просит её каждый прогон).
func sshNotifyKey(host, cmd string) string {
	sum := sha256.Sum256([]byte(host + "\n" + strings.TrimSpace(cmd)))
	return "ssh:" + host + ":" + hex.EncodeToString(sum[:6])
}

// notifyMCPPending — вызов чужого сервера ждёт подтверждения. Право менять
// что-то у чужого сервиса человек даёт каждый раз отдельно.
func (e *Engine) notifyMCPPending(id int64, m *store.MCPServer, tool, args string, o MCPOrigin) {
	who := o.Who
	actions := []store.NotifyAction{
		{Label: "Выполнить", Kind: "core", Op: "approve_mcp", Ref: strconv.FormatInt(id, 10), Style: "primary"},
		{Label: "Отклонить", Kind: "core", Op: "reject_mcp", Ref: strconv.FormatInt(id, 10)},
		// впредь без вопроса — правило «Разрешить» этому инструменту этого сервера
		{Label: "Выполнить и разрешить впредь", Kind: "core", Op: "approve_mcp_always", Ref: strconv.FormatInt(id, 10)},
	}
	if o.ChatID > 0 {
		// вызов просил чат: результат вернётся туда, и открыть его отсюда — одна кнопка
		actions = append(actions, store.NotifyAction{Label: "Открыть чат", Kind: "goto", Screen: store.ScreenChat(o.ChatID)})
	}
	where := m.Name
	if m.URL != "" {
		where += " · " + m.URL
	}
	_, _ = e.Notify(&store.Notification{
		Key: fmt.Sprintf("mcp:%d", id), Source: "core", Level: "warn",
		Title:   m.Name + " · " + tool,
		Summary: "просил " + who + " · вызов меняет данные на чужом сервере",
		Fields: []store.NotifyField{
			{Label: "Сервер", Value: where},
			{Label: "Инструмент", Value: tool},
			{Label: "Аргументы", Value: clip(args, 800)},
			{Label: "Кто просил", Value: who},
		},
		Actions: actions,
		Ref:     fmt.Sprintf("mcp:%d", id),
	})
}

// setNotificationResultByKey — дописать итог в уведомление по ключу.
func (e *Engine) setNotificationResultByKey(key string, field store.NotifyField) error {
	n, ok := e.store.OpenNotificationByKey(key)
	if !ok {
		return nil
	}
	state := store.NotifyDone
	summary := "выполнено"
	if field.Level == "alert" {
		summary = "не вышло"
	}
	return e.store.SetNotificationResult(n.ID, field, summary, field.Level, state, "человек")
}

// notifySSHPending — команда ждёт подтверждения.
// почемуЖдёт — одной фразой: из-за чего команда стоит и что с этим
// делать. Без неё в записи было только «сейчас не окно», и человек не
// понимал, ждать ему или нажимать (13 сентября 2026).
func whyWaits(e *Engine, p *store.SSHPending) string {
	if line := e.WindowMismatch(p.Host); line != "" {
		return line + ". Нажмите «Выполнить» — Сёма сделает сразу; или поменяйте окно сервера либо час задачи, которая их присылает"
	}
	h, has := e.store.Host(p.Host)
	if !has || h == nil || !h.Auto {
		return "сервер на ручном управлении: изменения делает только человек"
	}
	if h.Paused != "" {
		return "сервер остановлен (" + h.Paused + ") — самостоятельная работа выключена, пока вы не нажмёте «Продолжить»"
	}
	return "изменяющая команда: Сёма её не выполняет без вашей кнопки"
}

func (e *Engine) notifySSHPending(p *store.SSHPending, why string) {
	_, _ = e.Notify(&store.Notification{
		Key: sshNotifyKey(p.Host, p.Cmd), Source: "core", Level: "warn",
		Title:   p.Host + " · " + clip(p.Cmd, 60),
		Summary: "просил " + p.Who + " · " + why,
		Fields: []store.NotifyField{{Label: "Команда", Value: p.Cmd}, {Label: "Сервер", Value: p.Host}, {Label: "Кто просил", Value: p.Who},
			{Label: "Почему ждёт", Value: whyWaits(e, p), Level: "warn"}},
		Actions: []store.NotifyAction{
			{Label: "Выполнить", Kind: "core", Op: "approve_ssh", Ref: strconv.FormatInt(p.ID, 10), Style: "primary"},
			{Label: "Отклонить", Kind: "core", Op: "reject_ssh", Ref: strconv.FormatInt(p.ID, 10)},
			// команда ждёт из-за окна — человек должен видеть, где это
			// поправить, а не искать настройку по экранам
			{Label: "Окно сервера", Kind: "goto", Screen: ServerScreen(p.Host, "auto")},
		},
		Ref: fmt.Sprintf("ssh:%d", p.ID),
	})
}

// notifyFingerprint — отпечаток сервера сменился.
func (e *Engine) notifyFingerprint(h *store.Host) {
	_, _ = e.Notify(&store.Notification{
		Key: "fp:" + h.Name, Source: "core", Level: "alert",
		Title:   h.Name + " · отпечаток сервера сменился",
		Summary: "сервер переустанавливали? команды не выполняются, пока не примете новый",
		Fields:  []store.NotifyField{{Label: "Было", Value: h.Fingerprint}, {Label: "Стало", Value: h.NewFP, Level: "alert"}},
		Actions: []store.NotifyAction{
			{Label: "Принять новый", Kind: "core", Op: "accept_fp", Ref: h.Name, Style: "primary"},
			{Label: "Это тревога", Kind: "core", Op: "reject_fp", Ref: h.Name},
		},
	})
}

// notifySkillProposal — предложение скилла из чата.
func (e *Engine) notifySkillProposal(id string, k *store.Skill, isNew bool, diff, who string) {
	what := "изменение"
	if isNew {
		what = "новый скилл"
	}
	_, _ = e.Notify(&store.Notification{
		Key: "skill:" + id, Source: "core", Level: "info",
		Title:   "Скилл «" + k.Title + "» · " + what,
		Summary: "предложил " + who,
		Fields:  []store.NotifyField{{Label: "Разница", Value: diff}},
		Actions: []store.NotifyAction{
			{Label: "Принять", Kind: "core", Op: "decide_skill", Ref: id, Arg: "accept", Style: "primary"},
			{Label: "Отклонить", Kind: "core", Op: "decide_skill", Ref: id, Arg: "reject"},
			{Label: "Открыть скилл", Kind: "goto", Screen: SkillScreen(k.Name)},
		},
	})
}

// NotifyModuleGrants — модуль просит права: кнопка ведёт на экран прав.
func (e *Engine) NotifyModuleGrants(m *Module, what []string) {
	if len(what) == 0 {
		return
	}
	_, _ = e.Notify(&store.Notification{
		Key: "grants:" + m.Name, Source: "core", Level: "info",
		Title:   "Модуль «" + m.Title + "» просит " + strings.Join(what, ", "),
		Summary: "права выдаются на экране «Модули» тумблерами",
		Actions: []store.NotifyAction{{Label: "Открыть модули", Kind: "goto", Screen: "modules", Style: "primary"}, {Label: "Позже", Kind: "dismiss"}},
	})
}

// notifyBudget — бюджет исчерпан.
func (e *Engine) notifyBudget(consumer, what string, paused bool) {
	level := "warn"
	summary := "запросы к модели отклоняются до конца периода"
	if paused {
		level, summary = "alert", "потребитель поставлен на паузу — включите на «Модулях» или «Задачах», когда решите"
	}
	_, _ = e.Notify(&store.Notification{
		Key: "budget:" + consumer, Source: "core", Level: level,
		Title: what, Summary: summary,
		Actions: []store.NotifyAction{{Label: "Открыть «Здоровье»", Kind: "goto", Screen: "health", Style: "primary"}, {Label: "Понял", Kind: "core", Op: "reset_budget"}},
	})
}

// ---- модули ----

// notifyTools — sema.notify и sema.remind без отдельного права; серверы в
// действиях — только выданные модулю, чужие кнопки выбрасываются.
func (e *Engine) notifyTools(consumer string) []script.Tool {
	source := consumer
	var granted []string
	isModule := false
	if name, ok := strings.CutPrefix(consumer, "module:"); ok {
		isModule = true
		if m, found := e.Module(name); found {
			granted = m.GrantedHosts
		}
	}
	return []script.Tool{
		{
			Name: "notify", Need: "", Args: "({key, title, level, summary, fields, actions, due})",
			Desc: "уведомление человеку одной формой: level ok|info|warn|alert, fields [{label, value, level}], actions [{label, ssh, cmd} | {label, screen}], тот же key заменяет прежнее",
			Fn: func(a []any) (any, error) {
				opts := script.Opts(a, 0)
				n := notificationFromOpts(opts, source)
				if n.Title == "" {
					return nil, fmt.Errorf("у уведомления нужен title")
				}
				var actions []store.NotifyAction
				for _, act := range n.Actions {
					if act.Kind == "ssh" && isModule && !hasString(granted, act.Host) {
						log.Printf("%s: команда на сервер %q не попала в уведомление — сервер не выдан", consumer, act.Host)
						continue
					}
					actions = append(actions, act)
				}
				n.Actions = actions
				id, err := e.Notify(n)
				if err != nil {
					return nil, err
				}
				return map[string]any{"id": id, "key": n.Key}, nil
			},
		},
		{
			Name: "remind", Need: "", Args: "(text, when, source)",
			Desc: "напомнить: текст, срок словами («в пятницу», «10 сентября», «через 3 дня»), source — id письма или пусто",
			Fn: func(a []any) (any, error) {
				return e.toolRemind(source, script.Str(a, 0), script.Str(a, 1), script.Str(a, 2))
			},
		},
		{
			Name: "attention.remind", Need: "", Args: "(text, when, source)",
			Desc: "то же, что sema.remind — старое имя",
			Fn: func(a []any) (any, error) {
				return e.toolRemind(source, script.Str(a, 0), script.Str(a, 1), script.Str(a, 2))
			},
		},
	}
}

func notificationFromOpts(o map[string]any, source string) *store.Notification {
	str := func(k string) string {
		if v, ok := o[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	n := &store.Notification{Source: source, Key: str("key"), Title: str("title"), Level: str("level"), Summary: str("summary"), Due: str("due")}
	if n.Key == "" {
		n.Key = source + ":" + clip(n.Title, 40)
	}
	if raw, ok := o["fields"].([]any); ok {
		for _, f := range raw {
			if m, ok := f.(map[string]any); ok {
				n.Fields = append(n.Fields, store.NotifyField{Label: fmt.Sprint(m["label"]), Value: fmt.Sprint(m["value"]), Level: strOf(m["level"])})
			}
		}
	}
	if raw, ok := o["actions"].([]any); ok {
		for _, f := range raw {
			m, ok := f.(map[string]any)
			if !ok {
				continue
			}
			act := store.NotifyAction{Label: strOf(m["label"])}
			switch {
			case strOf(m["ssh"]) != "" || strOf(m["host"]) != "":
				act.Kind, act.Host, act.Cmd = "ssh", strOf(m["ssh"])+strOf(m["host"]), strOf(m["cmd"])
				if act.Label == "" {
					act.Label = "Выполнить"
				}
			case strOf(m["screen"]) != "":
				act.Kind, act.Screen = "goto", strOf(m["screen"])
			case strOf(m["fn"]) != "":
				act.Kind, act.Fn, act.Arg = "module", strOf(m["fn"]), strOf(m["arg"])
				act.Module = strings.TrimPrefix(source, "module:")
			default:
				act.Kind = "done"
			}
			n.Actions = append(n.Actions, act)
		}
	}
	if n.Due != "" {
		if t := script.ParseDate(n.Due); !t.IsZero() {
			n.Due = t.Format(time.RFC3339)
			n.RemindAt = n.Due
		}
	}
	return n
}

func strOf(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// toolRemind — напоминание со сроком словами; наступивший срок поднимает
// уведомление в alert.
func (e *Engine) toolRemind(source, text, when, ref string) (any, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("что напомнить?")
	}
	if when == "" {
		when = "завтра"
	}
	due := script.ParseDate(when)
	if due.IsZero() {
		return nil, fmt.Errorf("не понял срок %q", when)
	}
	if due.Hour() == 0 && due.Minute() == 0 {
		due = time.Date(due.Year(), due.Month(), due.Day(), 9, 0, 0, 0, due.Location())
	}
	// напоминание — это всегда срок: «оплатить сервер» с уровнем «инфо» не
	// проходило порог каналов и оставалось лежать на экране молча
	n := &store.Notification{
		Key: fmt.Sprintf("reminder:%d", time.Now().UnixNano()), Source: source, Level: "warn",
		Title: text, Summary: "срок " + humanWhen(due), Due: due.Format(time.RFC3339), RemindAt: due.Format(time.RFC3339),
		Actions: []store.NotifyAction{{Label: "Сделано", Kind: "done", Style: "primary"}, {Label: "Отклонить", Kind: "dismiss"}},
	}
	if ref != "" {
		if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 {
			n.Ref = "msg:" + ref
			n.Actions = append([]store.NotifyAction{{Label: "Открыть письмо", Kind: "goto", Screen: "msg:" + ref}}, n.Actions...)
		}
	}
	id, err := e.Notify(n)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "due": n.Due, "text": text, "when": humanWhen(due)}, nil
}

var ruMonths = []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
var ruWeekdays = []string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}

func humanWhen(t time.Time) string {
	if t.IsZero() {
		return "без срока"
	}
	s := fmt.Sprintf("%d %s", t.Day(), ruMonths[t.Month()-1])
	if t.Hour() != 9 || t.Minute() != 0 {
		s += t.Format(" 15:04")
	}
	days := int(t.Truncate(24*time.Hour).Sub(time.Now().Truncate(24*time.Hour)).Hours() / 24)
	if days >= 0 && days < 7 {
		s = ruWeekdays[t.Weekday()] + " " + s
	}
	return s
}

// ---- почтовые правила ----

// rulesSeed — метка досыпки правил: новое встроенное правило
// добавляется один раз, а не при каждом старте.
const rulesSeed = "notify.rules_seed"

var seedNotifyRules = []*store.NotifyRule{
	{ID: "receipts", Title: "Счета и чеки", Cond: "group:receipts", About: "группа дайджеста «счета и чеки», в письме есть срок оплаты", Kind: "due", Days: 3, Mode: "now", Sort: 10, Enabled: true},
	{ID: "people", Title: "От людей без ответа", Cond: "group:people", About: "письмо от человека с вопросом, ответа в архиве нет", Kind: "noreply", Days: 2, Mode: "suggest", Sort: 20, Enabled: true},
	{ID: "ics", Title: "Приглашения .ics", Cond: "ics", About: "вложение-календарь с датой встречи", Kind: "ics", Days: 0, Mode: "now", Sort: 30, Enabled: true},
	// Деньги и продления — раньше группы: письмо «не хватает средств для
	// автоматического продления» попадало в группу «Уведомления» с видом
	// «ничего не заводить» и не доходило до человека вовсе (13 сентября
	// 2026). Правило стоит выше группового нарочно: первое подходящее и
	// решает
	{ID: "payments", Title: "Счета, платежи и продления", Cond: "words:не хватает средств,недостаточно средств,пополнить баланс,пополните баланс,для продления,срок оплаты,к оплате,счёт на оплату,счет на оплату,оплатите,задолженность,istek,invoice,payment due",
		About: "письмо про деньги и сроки: продление, счёт, задолженность — приходит записью со сроком, даже если отправитель робот", Kind: "due", Days: 3, Mode: "now", Sort: 5, Enabled: true},
	{ID: "notifications", Title: "Уведомления роботов", Cond: "group:notifications", About: "роботы и рассылки — не порождают напоминаний", Kind: "none", Days: 0, Mode: "now", Sort: 40, Enabled: true},
}

func (e *Engine) ensureNotifySeed() {
	rules, _ := e.store.NotifyRules()
	if len(rules) == 0 {
		for _, r := range seedNotifyRules {
			_ = e.store.SaveNotifyRule(r)
		}
	} else if e.store.Setting(rulesSeed, "") == "" {
		// справочник в базе не обновляется сам: новое правило досыпается
		// один раз по метке, удалённое человеком не возвращаем
		has := map[string]bool{}
		for _, r := range rules {
			has[r.ID] = true
		}
		for _, r := range seedNotifyRules {
			if !has[r.ID] {
				_ = e.store.SaveNotifyRule(r)
			}
		}
		_ = e.store.SetSetting(rulesSeed, "1")
	}
	if chans, _ := e.store.NotifyChannels(); len(chans) == 0 {
		_ = e.store.SaveNotifyChannel(&store.NotifyChannel{Kind: "mac", Title: "Mac · системные уведомления", Enabled: true, MinLevel: "warn", Sources: "", QuietFrom: "23:00", QuietTo: "8:00"})
	}
}

func matchNotifyRule(r *store.NotifyRule, p map[string]any) bool {
	if !r.Enabled {
		return false
	}
	c := r.Cond
	switch {
	case c == "ics":
		return hasIcsFile(p)
	case strings.HasPrefix(c, "group:"):
		return strOf(p["group"]) == strings.TrimPrefix(c, "group:")
	case strings.HasPrefix(c, "sender:"):
		return strings.Contains(strings.ToLower(strOf(p["from"])), strings.ToLower(strings.TrimPrefix(c, "sender:")))
	case strings.HasPrefix(c, "words:"):
		s := strings.ToLower(strOf(p["subject"]) + " " + strOf(p["text"]))
		for _, w := range strings.Split(strings.TrimPrefix(c, "words:"), ",") {
			if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(s, w) {
				return true
			}
		}
	}
	return false
}

func hasIcsFile(p map[string]any) bool {
	files, _ := p["files"].([]map[string]any)
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(strOf(f["filename"])), ".ics") {
			return true
		}
	}
	return false
}

// Приглашение .ics говорит о времени тремя разными способами, и путать их
// нельзя. `20260912T140000Z` — время UTC; `;TZID=Europe/Moscow:20260912T140000`
// — время в названном поясе; без хвоста и без TZID — «плавающее», то есть
// местное там, где его открыли.
//
// До 10 сентября 2026 Сёма читала все три одинаково — как местное. Встреча
// в 14:00 UTC становилась 14:00 по Самаре, и напоминание приходило на
// четыре часа раньше самой встречи. Хуже того, ошибка неочевидна: время
// выглядит правдоподобно, просто оно чужое.
//
// `[^:\r\n]*` вместо `[^:]*` — тоже не мелочь: класс «не двоеточие» ловит
// и перевод строки, поэтому прежнее выражение могло взять «DTSTART» из
// одной строки и цифры из другой.
var reIcsStart = regexp.MustCompile(`DTSTART([^:\r\n]*):(\d{4})(\d{2})(\d{2})(?:T(\d{2})(\d{2})(\d{2})?(Z)?)?`)
var reIcsSummary = regexp.MustCompile(`SUMMARY[^:\r\n]*:([^\r\n]*)`)
var reIcsTZID = regexp.MustCompile(`(?i)TZID=\"?([^;:\"]+)`)

func parseIcs(text string) (time.Time, string, bool) {
	m := reIcsStart.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, "", false
	}
	pairs := m[1]
	y, _ := strconv.Atoi(m[2])
	mo, _ := strconv.Atoi(m[3])
	d, _ := strconv.Atoi(m[4])
	// без времени — событие на весь день. Ставим девять утра: напоминание
	// в полночь про сегодняшний день бесполезно
	h, mi := 9, 0
	if m[5] != "" {
		h, _ = strconv.Atoi(m[5])
		mi, _ = strconv.Atoi(m[6])
	}
	place := icsTime(pairs, m[8] == "Z")
	summary := "встреча"
	if sm := reIcsSummary.FindStringSubmatch(text); sm != nil {
		summary = strings.TrimSpace(sm[1])
	}
	// наружу отдаём местное: дальше это время сравнивают с «сейчас» и
	// показывают человеку
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, place).Local(), summary, true
}

// icsTime — в каком поясе записано время. Пояса вшиты в бинарник
// (`time/tzdata`), но чужой календарь может назвать пояс, которого нет:
// тогда честнее считать время местным, чем сдвигать его наугад.
func icsTime(pairs string, utc bool) *time.Location {
	if utc {
		return time.UTC
	}
	if tz := reIcsTZID.FindStringSubmatch(pairs); tz != nil {
		if loc, err := time.LoadLocation(strings.TrimSpace(tz[1])); err == nil {
			return loc
		}
		log.Printf("ics: пояс %q неизвестен — считаю время местным", strings.TrimSpace(tz[1]))
	}
	return time.Local
}

const extractDuePrompt = "Ты помощник, который находит в письме срок, к которому нужно что-то сделать (оплатить, ответить, прийти, продлить).\n" +
	"Сегодня %s. Письмо от %s, тема «%s»:\n---\n%s\n---\n" +
	"Ответь ТОЛЬКО JSON: {\"due\": \"ГГГГ-ММ-ДД или пусто\", \"text\": \"что сделать, коротко, по-русски, с суммой если есть\", \"amount\": \"сумма с валютой или пусто\"}. " +
	"Если срока в письме нет или это реклама («успейте до…»), due — пустая строка."

// applyMailRules — письмо разобрано: по правилам рождается уведомление.
func (e *Engine) applyMailRules(ctx context.Context, msgID int64) {
	p, err := e.eventPayload(EventMessageAdded, msgID)
	if err != nil {
		return
	}
	if collect, ok := p["collect"].(map[string]bool); ok && !collect["attention"] {
		return
	}
	status := strOf(p["status"])
	if status != "inbox_ok" && status != "ham" && status != "archived" {
		return
	}
	from0 := strOf(p["from"])
	// письмо об оплате закрывает дела того же отправителя: человек
	// заплатил, и напоминанию больше нечего ждать
	if n := e.closeSenderMatters(from0, strOf(p["subject"]), strOf(p["text"])); n > 0 {
		log.Printf("уведомления: %s подтвердил оплату — закрыто записей: %d", from0, n)
	}
	// Ключ называет дело, а не письмо. Провайдер присылает напоминание
	// каждый день, и по ключу «mail:<номер письма>» в «Уведомлениях»
	// висели три строки об одном продлении (13 сентября 2026). Тот же
	// ключ заменяет прежнюю открытую запись — это уже правило ядра
	key := fmt.Sprintf("mail:%d", msgID)
	if matter := messageMatter(from0, strOf(p["subject"])); matter != "" {
		key = matter
	}
	if previous, exists := e.store.OpenNotificationByKey(key); exists && previous.Ref == fmt.Sprintf("mail:%d", msgID) {
		return
	}
	rules, _ := e.store.NotifyRules()
	var rule *store.NotifyRule
	for _, r := range rules {
		if matchNotifyRule(r, p) {
			rule = r
			break
		}
	}
	if rule == nil || rule.Kind == "none" {
		return
	}
	from := strOf(p["fromName"])
	if from == "" {
		from = strOf(p["from"])
	}
	subject := strOf(p["subject"])
	open := store.NotifyAction{Label: "Открыть письмо", Kind: "goto", Screen: fmt.Sprintf("msg:%d", msgID), Style: "primary"}
	done := store.NotifyAction{Label: "Сделано", Kind: "done"}
	drop := store.NotifyAction{Label: "Не нужно", Kind: "dismiss"}
	// письмо со сроком — «внимание», а не «к сведению»: срок оплаты и час
	// встречи держат работу человека. Прежний «info» не проходил порог
	// каналов, и «Пополнить баланс для продления сервера» не уходило никуда.
	// Общая дверь (levelWithDue) всё равно поднимет, но намерение должно
	// быть видно там, где запись рождается
	level := "warn"
	switch rule.Kind {
	case "ics":
		files, _ := p["files"].([]map[string]any)
		for _, f := range files {
			if !strings.HasSuffix(strings.ToLower(strOf(f["filename"])), ".ics") {
				continue
			}
			if due, summary, ok := parseIcs(strOf(f["text"])); ok {
				remind := time.Date(due.Year(), due.Month(), due.Day(), 9, 0, 0, 0, due.Location())
				_, _ = e.Notify(&store.Notification{
					Key: key, Source: "mail", Level: level, Ref: fmt.Sprintf("mail:%d", msgID),
					Title: "Встреча «" + summary + "» · " + humanWhen(due), Summary: "приглашение от " + from + " · «" + subject + "»",
					Due: due.Format(time.RFC3339), RemindAt: remind.Format(time.RFC3339),
					Fields:  []store.NotifyField{{Label: "Когда", Value: humanWhen(due)}, {Label: "От кого", Value: from}, {Label: "Правило", Value: rule.Title}},
					Actions: []store.NotifyAction{open, done, drop},
				})
				return
			}
		}
	case "noreply":
		date, _ := time.Parse(time.RFC3339, strOf(p["date"]))
		due := date.AddDate(0, 0, rule.Days)
		due = time.Date(due.Year(), due.Month(), due.Day(), 9, 0, 0, 0, time.Local)
		_, _ = e.Notify(&store.Notification{
			Key: key, Source: "mail", Level: "ok", Ref: fmt.Sprintf("mail:%d", msgID), // ждёт: поднимется задачей «Сроки», если ответа не будет
			Title: "Ответить " + from + " · «" + subject + "»", Summary: "письмо от человека · проверю ответ " + humanWhen(due),
			Due: due.Format(time.RFC3339), RemindAt: due.Format(time.RFC3339),
			Fields:  []store.NotifyField{{Label: "От кого", Value: from}, {Label: "Ящик", Value: strOf(p["account"])}, {Label: "Правило", Value: rule.Title}},
			Actions: []store.NotifyAction{open, {Label: "Ответил", Kind: "done"}, drop},
		})
	default: // due — срок из письма через модель
		local, _ := p["local"].(bool)
		prompt := fmt.Sprintf(extractDuePrompt, time.Now().Format("2006-01-02"), from, subject, clip(strOf(p["text"]), 3000))
		raw, err := e.toolLLM(ctx, prompt, map[string]any{"local": local})
		if err != nil {
			return
		}
		s := strOf(raw)
		a, b := strings.Index(s, "{"), strings.LastIndex(s, "}")
		if a < 0 || b < a {
			return
		}
		var v struct{ Due, Text, Amount string }
		if json.Unmarshal([]byte(s[a:b+1]), &v) != nil || v.Due == "" {
			return
		}
		due, err := time.ParseInLocation("2006-01-02", v.Due, time.Local)
		if err != nil {
			return
		}
		due = time.Date(due.Year(), due.Month(), due.Day(), 9, 0, 0, 0, time.Local)
		remind := due.AddDate(0, 0, -rule.Days)
		title := v.Text
		if title == "" {
			title = subject
		}
		if v.Amount != "" && !strings.Contains(title, v.Amount) {
			title += " · " + v.Amount
		}
		_, _ = e.Notify(&store.Notification{
			Key: key, Source: "mail", Level: level, Ref: fmt.Sprintf("mail:%d", msgID),
			Title: title, Summary: "срок " + humanWhen(due) + " · от " + from,
			Due: due.Format(time.RFC3339), RemindAt: remind.Format(time.RFC3339),
			Fields:  []store.NotifyField{{Label: "Срок", Value: humanWhen(due)}, {Label: "Сумма", Value: v.Amount}, {Label: "От кого", Value: from}, {Label: "Правило", Value: rule.Title}},
			Actions: []store.NotifyAction{open, done, drop},
		})
	}
}

// notifyDuePass — задача «Сроки»: наступившие напоминания поднимаются в
// alert, письма без ответа проверяются по архиву, старые закрытые убираются.
func (e *Engine) notifyDuePass(ctx context.Context) string {
	// записи, заведённые до того, как ключ стал называть дело: двойники
	// одного продления и то, что человек уже оплатил. Проход разовый и
	// живёт здесь, а не в старте сервера: задача «Сроки» о том же
	e.ConsolidateMattersOnce()
	// оплаченное закрывается само: подтверждение от отправителя приходит
	// когда придёт, и ждать его должен Сёма
	paid := e.ClosePaid()
	now := time.Now()
	due, _ := e.store.DueNotifications(now.Format(time.RFC3339))
	raised, answered := 0, 0
	for _, n := range due {
		if strings.HasPrefix(n.Ref, "msg:") && strings.HasPrefix(n.Title, "Ответить ") {
			if e.replyFound(n) {
				_ = e.store.SetNotificationState(n.ID, store.NotifyDone, "ответ найден в архиве")
				answered++
				continue
			}
		}
		summary := "срок наступил"
		if n.Due != "" {
			if t, err := time.Parse(time.RFC3339, n.Due); err == nil && t.Before(now.Add(-time.Hour)) {
				summary = "просрочено · срок был " + humanWhen(t)
			}
		}
		_ = e.store.UpdateNotificationLevel(n.ID, "alert", summary)
		if fresh, ok := e.store.Notification(n.ID); ok {
			e.deliver(fresh)
		}
		raised++
	}
	// счета, у которых срок прошёл, а чека в архиве нет
	invoices := e.paymentsPass(now)
	// и счета, которых нет, хотя обычно к этому числу уже были
	loss := e.missingBillsPass(now)
	// повтор недоставленного: посылка была одноразовой, и запись, не
	// ушедшая в тихий час или при спящем жителе машины, молчала навсегда
	resent := e.resendUndelivered(now)
	purged := e.store.PurgeNotifications(now.AddDate(0, 0, -e.NotifyKeepDays()).Format(time.RFC3339))
	chats := e.purgeExpiredChats(now)
	result := fmt.Sprintf("поднято: %d · ответы найдены: %d · убрано старых: %d · чатов команд закрыто: %d", raised, answered, purged, chats)
	if paid > 0 {
		result += fmt.Sprintf(" · закрыто оплаченных: %d", paid)
	}
	if invoices > 0 {
		result += fmt.Sprintf(" · счетов без чека: %d", invoices)
	}
	if loss > 0 {
		result += fmt.Sprintf(" · не пришло счетов: %d", loss)
	}
	if resent > 0 {
		result += fmt.Sprintf(" · повторно отправлено: %d", resent)
	}
	return result
}

// replyFound — есть ли в архиве наш ответ на письмо из уведомления.
//
// Сначала спрашиваем конверт: письмо, у которого `In-Reply-To` или
// `References` указывают на исходное, — это ответ, и другого толкования у
// него нет. Так стало возможно с 10 сентября 2026, когда Сёма перестала
// выбрасывать заголовки конверта.
//
// Совпадение темы осталось запасным путём — для писем, пришедших до этого
// дня, у которых конверта в базе нет. Путь этот плохой: «Re: Счёт» есть у
// десятка разных разговоров, — поэтому он второй, а не первый.
func (e *Engine) replyFound(n *store.Notification) bool {
	if id := notificationMessage(n); id > 0 {
		if m, err := e.store.Message(id); err == nil {
			if reply, err := e.store.ReplyTo(m.MessageID); err == nil && reply {
				return true
			}
		}
	}
	account := ""
	for _, f := range n.Fields {
		if f.Label == "Ящик" {
			account = f.Value
		}
	}
	subject := ""
	if i := strings.Index(n.Title, "«"); i >= 0 {
		subject = strings.Trim(n.Title[i:], "«»")
	}
	if account == "" || subject == "" {
		return false
	}
	subj := clip(regexp.MustCompile(`(?i)^(re|fw|fwd|отв)[:\s]+`).ReplaceAllString(subject, ""), 40)
	found, _ := e.toolMessages(map[string]any{"from": account, "subject": subj, "status": "any", "since": n.Created, "limit": 1})
	if list, ok := found.([]map[string]any); ok {
		return len(list) > 0
	}
	if list, ok := found.([]any); ok {
		return len(list) > 0
	}
	return false
}

// notificationMessage — номер письма из ссылки «msg:<id>».
func notificationMessage(n *store.Notification) int64 {
	// «mail:<id>» — запись из почтового правила, «msg:<id>» — напоминание
	// о письме. Ключ записи теперь называет дело, а не письмо, поэтому
	// номер письма живёт в Ref и читать его надо отсюда
	for _, p := range []string{"msg:", "mail:"} {
		if strings.HasPrefix(n.Ref, p) {
			id, _ := strconv.ParseInt(strings.TrimPrefix(n.Ref, p), 10, 64)
			return id
		}
	}
	return 0
}

// ---- каналы ----

// deliver рассылает по включённым каналам: Mac — через ленту клиента (ничего
// не шлёт), модуль-канал — функция deliver(n) с правом notify_channel.
func (e *Engine) deliver(n *store.Notification) {
	if n.State != store.NotifyOpen || n.Level == "ok" {
		return
	}
	chans, _ := e.store.NotifyChannels()
	group := NotifySourceGroup(n)
	now := time.Now()
	for _, c := range chans {
		if c.Kind == "web" {
			// порог и тихие часы у устройств свои (канальные — по умолчанию),
			// поэтому решает не общий отбор ниже, а сама отправка. Идёт в
			// своей нитке: у каждого устройства свой разговор со службой
			// пушей, и ждать их ответа тому, кто завёл запись, незачем
			go e.deliverWeb(context.Background(), n, c)
			continue
		}
		if why := channelTakes(c, n, group, now); why != "" {
			continue // причину увидит человек строкой у записи (deliveryNote)
		}
		switch {
		case c.Kind == "mac":
			// показывает житель машины по проводу; доставленной запись
			// считается только после его ответа «показал»
			go e.deliverMac(n)
		case strings.HasPrefix(c.Kind, "module:"):
			name := strings.TrimPrefix(c.Kind, "module:")
			payload, _ := json.Marshal(map[string]any{"id": n.ID, "title": n.Title, "level": n.Level, "summary": n.Summary, "fields": n.Fields, "channel": c.Config})
			_, err := e.RunModuleFor(context.Background(), name, "deliver("+string(payload)+")", "канал «"+c.Title+"»")
			errText := ""
			if err != nil {
				errText = err.Error()
			}
			e.store.MarkDelivered(n.ID, c.Kind, errText)
		}
	}
}

// reasonThreshold — отказ канала по уровню записи. Это единственная из
// причин, которая означает «так и задумано», а не «что-то сломалось»,
// и потому её видно в отдельной проверке.
const reasonThreshold = "порог канала выше уровня записи"

// ChannelTakes — берёт ли канал эту запись в этот час: пусто — берёт, иначе
// причина отказа. Та же проверка, что у рассылки (channelTakes); наружу выставлена
// затем, чтобы экран настроек и тесты спрашивали движок, а не пересчитывали порог,
// источники и тихие часы своими руками.
func ChannelTakes(c *store.NotifyChannel, n *store.Notification, now time.Time) string {
	return channelTakes(c, n, NotifySourceGroup(n), now)
}

// channelTakes — берёт ли канал эту запись сейчас. Пусто — берёт, иначе
// причина отказа человеческими словами. Одна функция и для рассылки, и для
// строки «не доставлено» на экране: два списка условий разошлись бы, и
// объяснение врало бы про доставку.
func channelTakes(c *store.NotifyChannel, n *store.Notification, group string, now time.Time) string {
	switch {
	case !c.Enabled:
		return "канал выключен"
	case store.LevelRank(n.Level) < store.LevelRank(c.MinLevel):
		return reasonThreshold
	case c.Sources != "" && !hasString(strings.Split(strings.ReplaceAll(c.Sources, " ", ""), ","), group):
		return "источник «" + group + "» не входит в список канала"
	case inQuietHours(c, now):
		// Mac дочитает ленту утром; модуль — не будим ночью. Запись при этом
		// не теряется: повтор ставит resendUndelivered
		return "тихие часы " + c.QuietFrom + "–" + c.QuietTo
	}
	return ""
}

// deliveryMinLevel — самый низкий порог среди включённых каналов: с этого
// уровня запись вообще кому-то предназначена. Пусто — каналов нет.
//
// У пушей порог бывает свой у устройства: если телефон просит «от инфо», а
// канал — «от внимание», запись уровня «инфо» ему предназначена.
func (e *Engine) deliveryMinLevel(chans []*store.NotifyChannel) string {
	best := ""
	lower := func(level string) {
		if best == "" || store.LevelRank(level) < store.LevelRank(best) {
			best = level
		}
	}
	for _, c := range chans {
		if !c.Enabled {
			continue
		}
		lower(c.MinLevel)
		if c.Kind == "web" {
			subs, _ := e.store.PushSubscriptions()
			for _, s := range subs {
				if s.MinLevel != "" {
					lower(s.MinLevel)
				}
			}
		}
	}
	return best
}

// DeliveryChecker — почему записи молчат. Каналы и отказы читаются один раз
// на весь список: на экране «Уведомления» строк три сотни, и запрос на
// каждую превратил бы объяснение в тормоз.
//
// Закрытые записи не проверяем: человек их уже разобрал, и «не доставлено»
// у сделанного — упрёк ни о чём.
func (e *Engine) DeliveryChecker(list []*store.Notification) func(*store.Notification) string {
	chans, _ := e.store.NotifyChannels()
	now := time.Now()
	var ids []int64
	for _, n := range list {
		if n.State == store.NotifyOpen && n.Undelivered() {
			ids = append(ids, n.ID)
		}
	}
	fails := e.store.NotifyFails(ids)
	subs, _ := e.store.PushSubscriptions()
	return func(n *store.Notification) string {
		if n == nil || n.State != store.NotifyOpen || n.Level == "ok" || !n.Undelivered() {
			return ""
		}
		if len(chans) == 0 {
			return "не доставлено: каналов доставки нет — запись ждёт только здесь"
		}
		var reasons []string
		byThreshold := true
		for _, c := range chans {
			group := NotifySourceGroup(n)
			why := channelTakes(c, n, group, now)
			if c.Kind == "web" && why != "" {
				// у устройства порог и тихие часы могут быть свои: канал
				// отказал, а телефон взял бы
				for _, s := range subs {
					if channelTakes(pushEffective(c, s), n, group, now) == "" {
						why = ""
						break
					}
				}
			}
			if why == "" {
				why = "не дошло"
				if last := fails[n.ID]; last != "" {
					why = clip(last, 120)
				}
			}
			if why != reasonThreshold {
				byThreshold = false
			}
			reasons = append(reasons, c.Title+": "+why)
		}
		// Все каналы отказались по порогу — это не беда, а замысел:
		// уровень записи ниже того, с которого канал вообще будит
		// человека. Такая запись и задумана экранной.
		//
		// 11 сентября 2026 на «Уведомлениях» стояло восемнадцать строк
		// подряд, и каждая начиналась с «не доставлено · Mac: порог
		// канала выше уровня записи · Chrome на маке: порог канала выше
		// уровня записи», а суть вопроса — какой файл и почему —
		// обрезалась за краем. Восемнадцать разных вопросов выглядели
		// как один спам. Молчание по порогу впереди не ставим.
		if byThreshold {
			return ""
		}
		return "не доставлено · " + strings.Join(reasons, " · ")
	}
}

// DeliveryWeek — сколько записей за неделю каналы должны были доставить и
// сколько не дошло ни одним. Порог — самый низкий среди включённых
// каналов: то, что каналы не берут нарочно, недоставкой не считается.
func (e *Engine) DeliveryWeek() (undelivered, total int, note string) {
	chans, _ := e.store.NotifyChannels()
	min := e.deliveryMinLevel(chans)
	if min == "" {
		return 0, 0, "каналов доставки нет: записи ждут только на экране"
	}
	since := time.Now().AddDate(0, 0, -7).Format(time.RFC3339)
	undelivered, total = e.store.UndeliveredSince(since, min)
	return undelivered, total, ""
}

// resendUndelivered — «ничего не терять» и про доставку. Посылка была
// одноразовой: тихие часы, спящий житель машины, выключенный на минуту
// канал — и запись молчала навсегда. Раз в час пробуем ещё раз то, что не
// дошло ни одним каналом и не успело устареть.
func (e *Engine) resendUndelivered(now time.Time) int {
	// то, что устройствам с пушами не ушло в их тихие часы, уходит после них
	pending := e.resendPushPending(now)
	chans, _ := e.store.NotifyChannels()
	min := e.deliveryMinLevel(chans)
	if min == "" {
		return pending
	}
	since := now.Add(-time.Duration(e.notifyRetryHours()) * time.Hour).Format(time.RFC3339)
	list, err := e.store.UndeliveredOpen(since, min, 50)
	if err != nil {
		return pending
	}
	sent := 0
	for _, n := range list {
		e.deliver(n)
		sent++
	}
	return sent + pending
}

// notifyRetryHours — сколько часов повторять доставку. Сутки: запись
// старше суток человек уже видел на экране, а вечный повтор пустого канала
// пишет в журнал круглые сутки.
func (e *Engine) notifyRetryHours() int {
	v, _ := strconv.Atoi(e.store.Setting("notify.retry_h", "24"))
	if v <= 0 {
		v = 24
	}
	return v
}

func inQuietHours(c *store.NotifyChannel, now time.Time) bool {
	if c.QuietFrom == "" || c.QuietTo == "" {
		return false
	}
	from, err1 := time.Parse("15:04", c.QuietFrom)
	to, err2 := time.Parse("15:04", c.QuietTo)
	if err1 != nil || err2 != nil {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	f := from.Hour()*60 + from.Minute()
	t := to.Hour()*60 + to.Minute()
	if f <= t {
		return cur >= f && cur < t
	}
	return cur >= f || cur < t
}

// ---- переезд из модуля «Требует внимания» ----

// migrateAttentionModule переносит напоминания и правила модуля в ядро и
// убирает модуль; его данные остаются в module_data до следующего изменения.
func (e *Engine) migrateAttentionModule() {
	if e.store.Setting("notify.migrated_attention", "") == "1" {
		return
	}
	docs, _ := e.store.FindModuleDocs("attention", "items", store.DocQuery{Limit: 1000})
	moved := 0
	for _, d := range docs {
		it := d.Doc
		state := store.NotifyOpen
		switch strOf(it["state"]) {
		case "done":
			state = store.NotifyDone
		case "dropped", "dismissed":
			state = store.NotifyDismissed
		}
		key := "reminder:" + d.ID
		if msg := strOf(it["msg"]); msg != "" && msg != "0" {
			key = "mail:" + msg
		}
		level := "info"
		if strOf(it["state"]) == "overdue" {
			level = "alert"
		}
		n := &store.Notification{
			Key: key, Source: "mail", Level: level, State: state, Title: strOf(it["text"]),
			Summary: "перенесено из модуля «Требует внимания»", Due: strOf(it["due"]), RemindAt: strOf(it["remindAt"]),
			Created: strOf(it["created"]), DecidedBy: strOf(it["doneBy"]),
			Actions: []store.NotifyAction{{Label: "Сделано", Kind: "done", Style: "primary"}, {Label: "Не нужно", Kind: "dismiss"}},
		}
		if msg := strOf(it["msg"]); msg != "" && msg != "0" {
			n.Ref = "msg:" + msg
			n.Actions = append([]store.NotifyAction{{Label: "Открыть письмо", Kind: "goto", Screen: "msg:" + msg}}, n.Actions...)
		}
		if n.Title == "" {
			continue
		}
		if _, err := e.store.PutNotification(n); err == nil {
			moved++
		}
	}
	rules, _ := e.store.FindModuleDocs("attention", "rules", store.DocQuery{Limit: 100})
	for _, d := range rules {
		r := d.Doc
		days, _ := strconv.Atoi(strOf(r["days"]))
		sort, _ := strconv.Atoi(strOf(r["sort"]))
		enabled := strOf(r["enabled"]) != "false"
		_ = e.store.SaveNotifyRule(&store.NotifyRule{ID: d.ID, Title: strOf(r["title"]), Cond: strOf(r["cond"]), About: strOf(r["about"]),
			Kind: strOf(r["kind"]), Days: days, Mode: strOf(r["mode"]), Sort: sort, Enabled: enabled})
	}
	if all, err := e.modulesMap(); err == nil {
		if _, ok := all["attention"]; ok {
			_ = e.dropModuleTasks("attention")
			delete(all, "attention")
			_ = e.saveModules(all)
		}
	}
	_ = e.store.SetSetting("notify.migrated_attention", "1")
	if moved > 0 || len(rules) > 0 {
		log.Printf("уведомления: перенесено из модуля «Требует внимания» %d записей и %d правил", moved, len(rules))
	}
}

// peerChatOfNotice — чат Сёмы с собеседником, о котором уведомление.
// Ключи вида «tg:<чат>», «tg:<чат>/<тема>», «tg-wait:…», «max:<чат>» —
// это переписка, и разбирать её надо там же, где она лежит.
func (e *Engine) peerChatOfNotice(key string) int64 {
	kind, raw, ok := strings.Cut(key, ":")
	if !ok {
		return 0
	}
	switch kind {
	case "tg", "tg-wait", "tg-auto":
		// «tg:+7999…:7/3»: учётка, чат, тема. Старый ключ без учётки
		// понимается тоже — записи, заведённые до второй учётки, не
		// должны потерять кнопку «Разобраться»
		account, id, topic, ok := e.tgFromLinks(raw)
		if !ok {
			return 0
		}
		if c, found := e.store.TGChat(account, id); found {
			if topic > 0 {
				// разговор темы: разбирать надо в нём, у группы ленты нет
				return e.tgConvExisting(e.tgEffective(c, topic))
			}
			chat, err := e.tgChatFor(c)
			if err == nil {
				return chat
			}
		}
		return 0
	}
	topic := 0
	if left, right, ok := strings.Cut(raw, "/"); ok {
		n, err := strconv.Atoi(right)
		if err != nil {
			return 0
		}
		raw, topic = left, n
	}
	_ = topic
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	switch kind {
	case "max":
		if c, found := e.store.MAXChat(id); found {
			chat, err := e.maxChatFor(c)
			if err == nil {
				return chat
			}
		}
	}
	return 0
}

// hasTalk — есть ли в чате хоть одна реплика человека или Сёмы. Служебные
// заметки и карточки за разговор не считаются: сводить в память нечего.
func hasTalk(msgs []*store.ChatMsg) bool {
	for _, m := range msgs {
		if m.Role == "user" || m.Role == "assistant" {
			return true
		}
	}
	return false
}

// RemindAboutMessage — «вернуться к письму позже». Письмо при этом не
// прячется и не меняет состояния: оно остаётся там, где лежит, и его
// по-прежнему видно в поиске и в сводке. Прятать почту, чтобы «не мешала»,
// — верный способ её потерять.
//
// Напоминание — обычная запись «Уведомлений» со сроком: её поднимет задача
// «Сроки», как и всякую другую. Отдельного «отложенного» состояния у
// письма нет нарочно — второй механизм расходился бы с первым.
func (e *Engine) RemindAboutMessage(id int64, spec string) error {
	m, err := e.store.Message(id)
	if err != nil {
		return fmt.Errorf("письма %d нет", id)
	}
	when := script.ParseDate(spec)
	if when.IsZero() || !when.After(time.Now()) {
		return fmt.Errorf("не понял, когда напомнить: %q", spec)
	}
	from := m.FromName
	if from == "" {
		from = m.FromAddr
	}
	// тема секретного письма в напоминание не идёт: запись видна в списке
	// и уходит в канал доставки, а он проходит через чужую службу
	subject := clip(m.Subject, 120)
	if e.ClassOfMessage(m) == store.ClassSecret {
		subject = "письмо помечено секретным"
	}
	_, err = e.Notify(&store.Notification{
		Key: "msg-later:" + strconv.FormatInt(id, 10), Source: "mail", Level: "info",
		Ref:   "msg:" + strconv.FormatInt(id, 10),
		Title: "Вернуться к письму: " + subject, Summary: "от " + from,
		RemindAt: when.Format(time.RFC3339), Due: when.Format(time.RFC3339),
		Actions: []store.NotifyAction{
			{Label: "Открыть письмо", Kind: "goto", Screen: "msg:" + strconv.FormatInt(id, 10)},
			{Label: "Разобрался", Kind: "done"},
		},
	})
	return err
}

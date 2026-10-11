package api

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Действия почты: разбор спама, отправители, ящики, папки, задачи ящика.
// Часть общего разбора действий: execute перебирает такие группы, пока
// одна из них не возьмёт действие себе; чужое действие — errUnhandled.

func (s *Server) mailActions(req actionRequest) (string, error) {
	msgIDs := func() ([]int64, error) { return s.messagesByTarget(req.Target) }
	switch req.Action {
	case "confirm_spam":
		ids, err := msgIDs()
		if err != nil {
			return "", err
		}
		return "", s.eng.ConfirmSpam(ids)
	case "not_spam":
		ids, err := msgIDs()
		if err != nil {
			return "", err
		}
		return "", s.eng.MarkHam(ids)
	case "not_spam_sender", "confirm_spam_sender":
		// решение на всех письмах отправителя разом: сорок одинаковых
		// писем от Ozon — сорок нажатий, и человек их просто не делал
		адрес := strings.ToLower(strings.TrimSpace(req.Target))
		if адрес == "" {
			return "", fmt.Errorf("не назван отправитель")
		}
		письма, err := s.eng.Store().PendingFromSender(адрес, 500)
		if err != nil {
			return "", err
		}
		if len(письма) == 0 {
			return "", fmt.Errorf("у «%s» нет писем, ждущих решения", адрес)
		}
		ids := make([]int64, 0, len(письма))
		for _, m := range письма {
			ids = append(ids, m.ID)
		}
		if req.Action == "not_spam_sender" {
			if err := s.eng.MarkHam(ids); err != nil {
				return "", err
			}
			s.mu.Lock()
			s.lastReply = fmt.Sprintf("возвращено из спама: %s от «%s» · отправитель в белом списке",
				plural(len(ids), "письмо", "письма", "писем"), адрес)
			s.mu.Unlock()
			return "", nil
		}
		if err := s.eng.ConfirmSpam(ids); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.lastReply = fmt.Sprintf("в спам: %s от «%s»", plural(len(ids), "письмо", "письма", "писем"), адрес)
		s.mu.Unlock()
		return "", nil
	case "msg_chat":
		id, err := s.eng.MessageChat(atoi64(req.Target))
		if err != nil {
			return "", err
		}
		return "chat:" + strconv.FormatInt(id, 10), nil
	case "refresh":
		s.eng.TriggerPoll()
		return "", nil
	case "whitelist_off":
		return "", s.eng.SetWhitelistOnly(false)
	case "clean_inbox":
		n, err := s.eng.CleanInbox()
		if err != nil {
			return "", err
		}
		log.Printf("очистка входящих: в спам отправлено %d", n)
		return "", nil
	case "allow_sender":
		addr := strings.ToLower(strings.TrimSpace(req.Params["value"]))
		if addr == "" {
			return "", nil
		}
		return "senders", s.eng.Store().AddRule("sender", addr, "ham")
	case "save_mail_conn":
		c := s.eng.MailConf(req.Target)
		c.Auth = authByTitle(req.Params["auth"])
		for name, dst := range map[string]*string{
			"client_id": &c.ClientID, "tenant": &c.Tenant,
			"host": &c.Host, "username": &c.Username,
		} {
			if v, ok := req.Params[name]; ok {
				*dst = strings.TrimSpace(v)
			}
		}
		// пустое поле секрета означает «оставить прежний», а не «стереть»:
		// значение сюда и не приходит, пока человек не введёт новое
		for name, dst := range map[string]*string{"secret": &c.Secret, "password": &c.Password} {
			if v := strings.TrimSpace(req.Params[name]); v != "" {
				*dst = v
			}
		}
		if v, err := strconv.Atoi(req.Params["port"]); err == nil && v > 0 {
			c.Port = v
		}
		return "", s.eng.SaveMailConf(req.Target, c)
	case "goto_mailbox":
		// список в прежней закреплённой полосе: перейти к другому ящику, оставшись
		// на той же внутренней вкладке. Полосы больше нет (её место занял
		// entity_goto), но действие осталось ради закладок и скриптов
		tab, _ := mailBoxTab(req.Target)
		return entityAddr(mailScreen, strings.TrimSpace(req.Params["value"]), tab), nil
	case "skip_folders":
		// «Выключить все» из замечания: ярлыки Gmail, корзина и черновики
		// разбираются зря. Копии писем у Сёмы остаются
		c := s.eng.MailConf(req.Target)
		for _, f := range pointlessFolders(c) {
			c.SetSkipped(f, true)
		}
		return "", s.eng.SaveMailConf(req.Target, c)
	case "keep_folders":
		// «знаю, пусть разбирает»: замечание гаснет, но не навсегда —
		// изменится список папок или выйдет срок, и Сёма спросит снова
		return "", s.eng.KeepFolders(req.Target, pointlessFolders(s.eng.MailConf(req.Target)))
	case "connect_mailbox":
		if _, err := s.eng.ConnectMailbox(context.Background(), req.Target); err != nil {
			return "", err
		}
		// остаёмся там, где нажали: из строки списка, из «···» шапки и с
		// вкладки «Подключение» — три разных места, и ни одно не должно
		// уезжать на первую вкладку
		return "", nil
	case "toggle_folder":
		account, folder := splitTarget(req.Target)
		c := s.eng.MailConf(account)
		c.SetSkipped(folder, !c.Skipped(folder))
		return "", s.eng.SaveMailConf(account, c)
	case "save_mail_params":
		c := s.eng.MailConf(req.Target)
		if v := fetchDays(req.Params["fetch"]); v != 0 {
			c.FetchDays = v
		}
		if v, err := strconv.Atoi(req.Params["junk"]); err == nil && v > 0 {
			// только своему ящику. Раньше здесь же переписывалась общая
			// настройка — человек менял срок одному ящику, а менялось у
			// всех, и понять это было неоткуда: поле стояло на экране
			// ящика и называлось как настройка ящика.
			// Экран показывает срок, который действует сейчас: пока своего нет,
			// это общий. Он же приходит назад, когда меняют соседнее поле формы, —
			// и «как у всех» из-за этого не должно превращаться в своё
			if c.JunkDays != 0 || v != s.eng.SpamTTL() {
				c.JunkDays = v
			}
		}
		if _, has := req.Params["purge_other"]; has {
			c.PurgeOther = parseThree(req.Params["purge_other"])
		}
		if v, err := strconv.Atoi(req.Params["keep_days"]); err == nil && v >= 0 {
			c.KeepDays = v // ноль значит «как у всех»
		}
		return "", s.eng.SaveMailConf(req.Target, c)
	case "rescan_attachments":
		if _, err := s.eng.Store().ResetAttScan(req.Target); err != nil {
			return "", err
		}
		if accts := s.eng.Accounts(); len(accts) > 0 && accts[0].Name == req.Target {
			_, _ = s.eng.Store().ResetAttScan("") // письма основного ящика хранятся без имени
		}
		return "", nil
	case "save_mail_targets":
		c := s.eng.MailConf(req.Target)
		c.Archive = req.Params["archive"]
		c.Junk = req.Params["junk"]
		return "", s.eng.SaveMailConf(req.Target, c)
	case "attach_task":
		c := s.eng.MailConf(req.Target)
		tasks, err := s.eng.Store().Tasks()
		if err != nil {
			return "", err
		}
		for _, t := range tasks {
			if t.Title == req.Params["task"] {
				c.Tasks = append(c.Tasks, t.ID)
				return "", s.eng.SaveMailConf(req.Target, c)
			}
		}
		return "", nil
	case "toggle_mail_task":
		account, raw := splitTarget(req.Target)
		id, _ := strconv.ParseInt(raw, 10, 64)
		c := s.eng.MailConf(account)
		kept := c.Tasks[:0]
		found := false
		for _, t := range c.Tasks {
			if t == id {
				found = true
				continue
			}
			kept = append(kept, t)
		}
		c.Tasks = kept
		if !found {
			c.Tasks = append(c.Tasks, id)
		}
		return "", s.eng.Store().SaveMailConf(account, c)
	case "detach_task":
		account, raw := splitTarget(req.Target)
		id, _ := strconv.ParseInt(raw, 10, 64)
		c := s.eng.MailConf(account)
		kept := c.Tasks[:0]
		for _, t := range c.Tasks {
			if t != id {
				kept = append(kept, t)
			}
		}
		c.Tasks = kept
		return "", s.eng.SaveMailConf(account, c)
	case "reply_msg":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		// «сейчас» — только когда режим не «черновик»: кнопка на экране
		// подписана тем, что случится, и другого пути к отправке нет
		сейчас := s.eng.SendMode() != engine.ModeDraft
		исход, err := s.eng.ReplyToMessage(id, req.Params["текст"], сейчас)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.lastReply = "ответ " + исход.Where
		if исход.Folder != "" {
			s.lastReply += " · папка «" + исход.Folder + "»"
		}
		s.mu.Unlock()
		return "msg:" + req.Target, nil
	case "snooze_msg":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		когда := strings.TrimSpace(req.Params["когда"])
		if когда == "" || когда == "—" {
			return "", nil
		}
		if err := s.eng.RemindAboutMessage(id, когда); err != nil {
			return "", err
		}
		return "msg:" + req.Target, nil
	case "save_mail_send":
		acct := req.Target
		c := s.eng.MailConf(acct)
		if c == nil {
			return "", fmt.Errorf("ящик %q не настроен", acct)
		}
		c.SMTPHost = strings.TrimSpace(req.Params["smtp_host"])
		c.SMTPUser = strings.TrimSpace(req.Params["smtp_user"])
		if v := strings.TrimSpace(req.Params["smtp_port"]); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return "", fmt.Errorf("«%s» — не номер порта", v)
			}
			c.SMTPPort = n
		} else {
			c.SMTPPort = 0
		}
		if v, ok := req.Params["smtp_starttls"]; ok {
			c.SMTPStartTLS = v == "true"
		}
		// «—» значит «секрет не выбран»: тогда пароль берётся тот же, что
		// для чтения, а не первый попавшийся из хранилища
		if v := strings.TrimSpace(req.Params["smtp_secret"]); v != "" && v != "—" {
			c.SMTPSecret = v
		} else {
			c.SMTPSecret = ""
		}
		if err := s.eng.SaveMailConf(acct, c); err != nil {
			return "", err
		}
		return "", nil
	case "save_purge_other":
		// стирание обычной переписки сохраняется своей кнопкой: тумблер,
		// удаляющий вашу почту с сервера, не должен уезжать вместе с
		// числом дней одним нажатием «Сохранить»
		if _, has := req.Params["purge_other"]; has {
			if err := s.eng.SetPurgeRest(req.Params["purge_other"] == "true"); err != nil {
				return "", err
			}
		}
		if v, err := strconv.Atoi(req.Params["keep_days"]); err == nil {
			if err := s.eng.SetRestTTL(v); err != nil {
				return "", err
			}
		}
		if v, err := strconv.Atoi(req.Params["server_votes"]); err == nil {
			if err := s.eng.SetServerSpamVotes(v); err != nil {
				return "", err
			}
		}
		return "", nil
	case "save_automation":
		// общие настройки разъехались по четырём формам разных вкладок, и
		// каждая шлёт только свои поля. Непришедшее поле значит «эту
		// настройку не трогали», а не «выключить»: без проверки сохранение
		// сроков хранения гасило бы белый список
		if _, has := req.Params["whitelist"]; has {
			if err := s.eng.SetWhitelistOnly(req.Params["whitelist"] == "true"); err != nil {
				return "", err
			}
		}
		if v, err := strconv.Atoi(req.Params["keep_days"]); err == nil {
			if err := s.eng.SetRestTTL(v); err != nil {
				return "", err
			}
		}
		if _, ok := req.Params["purge_other"]; ok {
			if err := s.eng.SetPurgeRest(req.Params["purge_other"] == "true"); err != nil {
				return "", err
			}
		}
		if _, ok := req.Params["server_folders"]; ok {
			if err := s.eng.SetReadServerFolders(req.Params["server_folders"] == "true"); err != nil {
				return "", err
			}
		}
		if v := strings.TrimSpace(req.Params["send_mode"]); v != "" {
			// на экране «сам», в базе «сама»: принимаются оба слова
			if err := s.eng.SetSendMode(sendModeByTitle(v)); err != nil {
				return "", err
			}
		}
		if _, ok := req.Params["server_votes"]; ok {
			// порог «после скольких писем у провайдера адрес становится спамером»
			// жил на вкладке «Спам», а сохранялся только в save_purge_other, и
			// правка молча терялась (2 октября 2026, найдено проверкой «изменил —
			// движок видит»). Пустое поле — «не трогали», как у остальных
			if v, err := strconv.Atoi(strings.TrimSpace(req.Params["server_votes"])); err == nil {
				if err := s.eng.SetServerSpamVotes(v); err != nil {
					return "", err
				}
			} else if strings.TrimSpace(req.Params["server_votes"]) != "" {
				return "", fmt.Errorf("порог писем от провайдера — целое число от 0 до 50")
			}
		}
		if v, ok := req.Params["send_auto_to"]; ok {
			if err := s.eng.SetSendAutoTo(v); err != nil {
				return "", err
			}
		}
		if v, ok := req.Params["money_guard"]; ok {
			if err := s.eng.SetMoneyGuard(v == "true"); err != nil {
				return "", err
			}
		}
		if v, ok := req.Params["money_words"]; ok && strings.TrimSpace(v) != "" {
			if err := s.eng.SetMoneyWords(v); err != nil {
				return "", err
			}
		}
		if v, err := strconv.Atoi(req.Params["threshold"]); err == nil {
			if err := s.eng.SetAutoThreshold(float64(v) / 100); err != nil {
				return "", err
			}
		}
		if v, err := strconv.Atoi(req.Params["retention"]); err == nil {
			if err := s.eng.SetRetentionDays(v); err != nil {
				return "", err
			}
		}
		return "", nil
	case "restore_sender":
		return "senders", s.eng.RestoreSender(req.Target)
	case "restore_receipts":
		n, err := s.eng.RestoreSendersWithReceipts()
		if err != nil {
			return "", err
		}
		log.Printf("возвращено отправителей с чеками: %d", n)
		return "senders", nil
	case "forget_sender":
		return "senders", s.eng.ForgetSender(req.Target)
	case "save_mail_modes":
		c := s.eng.MailConf(req.Target)
		c.Purpose = purposeByTitle(req.Params["purpose"])
		c.Mode = modeByTitle(req.Params["mode"])
		outbound := req.Params["outbound"] == "true"
		c.Outbound = &outbound
		// признаки сбора приезжают полями «сбор:<имя>»: их состав знает
		// движок, а не этот разбор — имён модулей здесь нет
		for _, f := range s.eng.MailFlags() {
			c.SetCollect(f.Name, req.Params["сбор:"+f.Name] == "true")
		}
		// глубина «Входящих» стоит в той же форме, что и режим: слово пришло — так
		// и пишем, но «как у всех» от самой формы не превращается в своё (то же,
		// что у срока спама в save_mail_params)
		if v := fetchDays(req.Params["fetch"]); v != 0 && (c.FetchDays != 0 || v != s.mailFetchDefault(req.Target)) {
			c.FetchDays = v
		}
		return "", s.eng.SaveMailConf(req.Target, c)
	case "add_account":
		email := strings.TrimSpace(strings.ToLower(req.Params["email"]))
		// адрес экрана ящика строится из его имени: «~» в нём оборвал бы адрес, а
		// «new» заняло бы адрес экрана добавления
		if strings.Contains(email, "~") {
			return "", fmt.Errorf("«~» в адресе ящика не поддерживается: экран ящика строится по адресу")
		}
		if err := s.eng.AddAccount(mailPresetFor(req.Params["preset"]), email, req.Params["password"], req.Params["host"], purposeByTitle(req.Params["purpose"])); err != nil {
			return "", err
		}
		return entityAddr(mailScreen, email, ""), nil
	case "remove_account":
		return "", s.eng.RemoveAccount(req.Target)
	default:
		return "", errUnhandled
	}
}

// messagesByTarget — номера писем из цели группового действия.
//
// Отмеченной строкой на «Почте» бывает не письмо, а группа отправителя:
// её id — «спам-<адрес>», и «В спам выбранные» с такой отметкой падал в
// strconv.ParseInt прямо на экран (13 сентября 2026). Группа в выборе
// значит «все ждущие письма этого отправителя» — так её и читаем.
func (s *Server) messagesByTarget(target string) ([]int64, error) {
	var out []int64
	for _, p := range strings.Split(target, ",") {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
		case strings.HasPrefix(p, "спам-"):
			addr := strings.ToLower(strings.TrimPrefix(p, "спам-"))
			письма, err := s.eng.Store().PendingFromSender(addr, 500)
			if err != nil {
				return nil, err
			}
			for _, m := range письма {
				out = append(out, m.ID)
			}
		default:
			id, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("в выборе не письмо: %q", p)
			}
			out = append(out, id)
		}
	}
	return out, nil
}

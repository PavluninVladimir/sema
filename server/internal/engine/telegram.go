package engine

// Telegram как источник: половина переписки живёт не в почте. Ключи
// приложения и сессия лежат в хранилище секретов, наружу не выдаются ни
// модели, ни модулю. По умолчанию не собирается ничего, пока человек не
// выбрал чаты.

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

const (
	tgAppIDSecret   = "tg:api_id"
	tgAppHashSecret = "tg:api_hash"
)

// tgCodeHashKey — хэш отправленного кода, живёт между шагами входа. Ключ
// свой у каждой учётки: вход в рабочую не должен сбивать вход в личную.
func tgCodeHashKey(phone string) string { return "tg.code_hash." + phone }

// normalNumber — номер в одном виде: плюс и цифры. Человек пишет и
// «+7 999 …», и «8999…», а номер — это ключ учётки, и два написания
// одного номера завели бы две разные учётки с одной сессией.
func normalNumber(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "+" + b.String()
}

// NumberDigits — номер без плюса: так учётка стоит в адресе экрана.
// Плюс в адресе пришлось бы прятать — в строке запроса он значит пробел.
func NumberDigits(phone string) string { return strings.TrimPrefix(phone, "+") }

// TGCreds — ключи приложения из хранилища секретов.
func (e *Engine) TGCreds() (tg.Creds, error) {
	idRaw, ok := e.store.SecretValue(tgAppIDSecret)
	if !ok {
		return tg.Creds{}, fmt.Errorf("нет api_id: возьмите его на my.telegram.org и сохраните на вкладке «Telegram»")
	}
	hash, ok := e.store.SecretValue(tgAppHashSecret)
	if !ok {
		return tg.Creds{}, fmt.Errorf("нет api_hash: возьмите его на my.telegram.org")
	}
	id, err := strconv.Atoi(strings.TrimSpace(idRaw))
	if err != nil || id == 0 {
		return tg.Creds{}, fmt.Errorf("api_id должен быть числом")
	}
	return tg.Creds{AppID: id, AppHash: strings.TrimSpace(hash)}, nil
}

// SaveTGCreds — ключи приложения ложатся в хранилище секретов, как всё
// остальное: значение потом не показывается нигде.
func (e *Engine) SaveTGCreds(appID, appHash string) error {
	if strings.TrimSpace(appID) == "" || strings.TrimSpace(appHash) == "" {
		return fmt.Errorf("нужны и api_id, и api_hash")
	}
	if _, err := strconv.Atoi(strings.TrimSpace(appID)); err != nil {
		return fmt.Errorf("api_id должен быть числом")
	}
	if err := e.store.SaveSecret(&store.Secret{Name: tgAppIDSecret, Kind: store.SecretToken,
		Note: "Telegram · api_id"}, strings.TrimSpace(appID)); err != nil {
		return err
	}
	return e.store.SaveSecret(&store.Secret{Name: tgAppHashSecret, Kind: store.SecretToken,
		Note: "Telegram · api_hash"}, strings.TrimSpace(appHash))
}

// tgClient — клиент с сессией названной учётки. Учётка называется
// всегда: «та единственная» кончилась вместе с одним аккаунтом, и
// молчаливый выбор первой попавшейся означал бы, что рабочее сообщение
// уйдёт из личной.
func (e *Engine) tgClient(phone string) (*tg.Client, *store.TGAccount, error) {
	creds, err := e.TGCreds()
	if err != nil {
		return nil, nil, err
	}
	acc, ok := e.store.TGAccount(phone)
	if !ok {
		return nil, nil, fmt.Errorf("учётки Telegram %s нет", phone)
	}
	return tg.New(creds, acc.Session), acc, nil
}

// tgClientOf — клиент для чата: учётка записана в самом чате.
func (e *Engine) tgClientOf(c *store.TGChat) (*tg.Client, *store.TGAccount, error) {
	if c == nil {
		return nil, nil, fmt.Errorf("чат не назван")
	}
	return e.tgClient(c.Account)
}

// TGAccounts — учётки для экранов.
func (e *Engine) TGAccounts() []*store.TGAccount {
	list, err := e.store.TGAccounts()
	if err != nil {
		log.Printf("telegram: список учёток не прочитан: %v", err)
		return nil
	}
	return list
}

// TGAccountByDigits — учётка по номеру из адреса экрана (без плюса).
// Пусто — первая по порядку: экран должен открываться и без адреса.
// Названный, но несуществующий номер — отказ, а не «первая попавшаяся»:
// действие над чужой учёткой хуже, чем отказ выполнить его.
func (e *Engine) TGAccountByDigits(digits string) (*store.TGAccount, bool) {
	list := e.TGAccounts()
	if len(list) == 0 {
		return nil, false
	}
	digits = NumberDigits(strings.TrimSpace(digits))
	if digits == "" {
		return list[0], true
	}
	for _, a := range list {
		if NumberDigits(a.Phone) == digits {
			return a, true
		}
	}
	return nil, false
}

// TGAccountOrFirst — то же для экранов: устаревшая ссылка на забытую
// учётку должна открывать экран, а не пустоту.
func (e *Engine) TGAccountOrFirst(digits string) (*store.TGAccount, bool) {
	if a, ok := e.TGAccountByDigits(digits); ok {
		return a, true
	}
	list := e.TGAccounts()
	if len(list) == 0 {
		return nil, false
	}
	return list[0], true
}

// saveSession кладёт свежую сессию обратно: после входа она меняется, и
// потерять её — значит проходить вход заново.
func (e *Engine) saveSession(c *tg.Client, acc *store.TGAccount, state, note string) error {
	acc.Session, acc.State, acc.Note = c.Session(), state, note
	return e.store.SaveTGAccount(acc)
}

// TGStartLogin — первый шаг: код на телефон. Учётки может ещё не быть —
// тогда она заводится здесь же, пустой строкой без сессии.
func (e *Engine) TGStartLogin(ctx context.Context, phone, title string) error {
	phone = normalNumber(phone)
	if phone == "" {
		return fmt.Errorf("нужен номер телефона")
	}
	creds, err := e.TGCreds()
	if err != nil {
		return err
	}
	acc, ok := e.store.TGAccount(phone)
	if !ok {
		acc = &store.TGAccount{Phone: phone, Enabled: true, Sort: len(e.TGAccounts())}
	}
	if t := strings.TrimSpace(title); t != "" {
		acc.Title = t
	}
	c := tg.New(creds, acc.Session)
	hash, err := c.SendCode(ctx, phone)
	if err != nil {
		_ = e.saveSession(c, acc, "fail", clip(err.Error(), 200))
		return err
	}
	if err := e.store.SetSetting(tgCodeHashKey(phone), hash); err != nil {
		return err
	}
	log.Printf("telegram: код отправлен на %s", phone)
	return e.saveSession(c, acc, "code", "код отправлен в Telegram на другое устройство")
}

// TGConfirmCode — второй шаг: код из приложения.
func (e *Engine) TGConfirmCode(ctx context.Context, phone, code string) error {
	c, acc, err := e.tgClient(normalNumber(phone))
	if err != nil {
		return err
	}
	hash := e.store.Setting(tgCodeHashKey(acc.Phone), "")
	if hash == "" {
		return fmt.Errorf("код устарел — попросите новый")
	}
	err = c.SignIn(ctx, acc.Phone, hash, strings.TrimSpace(code))
	if tg.NeedPassword(err) {
		return e.saveSession(c, acc, "password", "включён второй фактор — нужен пароль")
	}
	if err != nil {
		_ = e.saveSession(c, acc, "code", clip(err.Error(), 200))
		return err
	}
	return e.tgAfterLogin(ctx, c, acc)
}

// TGConfirmPassword — третий шаг, если включён второй фактор.
func (e *Engine) TGConfirmPassword(ctx context.Context, phone, password string) error {
	c, acc, err := e.tgClient(normalNumber(phone))
	if err != nil {
		return err
	}
	if err := c.SignInPassword(ctx, password); err != nil {
		_ = e.saveSession(c, acc, "password", clip(err.Error(), 200))
		return err
	}
	return e.tgAfterLogin(ctx, c, acc)
}

func (e *Engine) tgAfterLogin(ctx context.Context, c *tg.Client, acc *store.TGAccount) error {
	who, err := c.Ready(ctx)
	if err != nil {
		_ = e.saveSession(c, acc, "fail", clip(err.Error(), 200))
		return err
	}
	_ = e.store.SetSetting(tgCodeHashKey(acc.Phone), "")
	log.Printf("telegram: %s — вошли как %s", acc.Phone, who)
	if acc.Title == "" {
		acc.Title = who // человек назовёт её иначе, но «вошли как» лучше пустоты
	}
	return e.saveSession(c, acc, "ok", "вошли как "+who)
}

// TGRefreshChats — обновить список чатов. Выбор человека не затирается:
// `collect` и класс остаются на месте.
func (e *Engine) TGRefreshChats(ctx context.Context, phone string) (int, error) {
	c, acc, err := e.tgClient(phone)
	if err != nil {
		return 0, err
	}
	if acc.State != "ok" {
		return 0, fmt.Errorf("сначала войдите в учётку %s", acc.Name())
	}
	list, err := c.Chats(ctx, e.TGChatsMax())
	if err != nil {
		_ = e.saveSession(c, acc, "fail", clip(err.Error(), 200))
		return 0, err
	}
	for _, ch := range list {
		row := tgRowOf(acc.Phone, ch)
		if old, ok := e.store.TGChat(acc.Phone, ch.ID); ok {
			row.Collect, row.Class, row.Cursor, row.Count = old.Collect, old.Class, old.Cursor, old.Count
		}
		if err := e.store.SaveTGChat(row); err != nil {
			return 0, err
		}
		if row.Forum {
			// темы группы: по ним экран показывает права, а разговоры
			// получают названия
			if err := e.tgRefreshTopics(ctx, c, row); err != nil {
				log.Printf("telegram: темы «%s» не прочитаны: %v", row.Title, err)
			}
		}
	}
	acc.Last = time.Now().Format(time.RFC3339)
	return len(list), e.saveSession(c, acc, "ok", tgListNote(list))
}

// tgListNote — что прочитали, по видам. Одно число «чатов: 14» не
// говорило, что каналов среди них ноль, и полгода никто не замечал.
func tgListNote(list []tg.Chat) string {
	var users, groups, channels int
	for _, ch := range list {
		switch ch.Kind {
		case "user":
			users++
		case "group":
			groups++
		case "channel":
			channels++
		}
	}
	return fmt.Sprintf("чатов в списке: %d · людей %d, групп %d, каналов %d", len(list), users, groups, channels)
}

// TGSetChannelsCollect — «читать все каналы» / «не читать ни один»:
// одним действием по всем каналам, кроме заблокированных — «не трогать
// вовсе» сильнее массовой кнопки. Возвращает, у скольких каналов право
// изменилось; разговоры по ним заводит вызывающий.
func (e *Engine) TGSetChannelsCollect(account string, on bool) ([]*store.TGChat, error) {
	list, err := e.store.TGChats(account, false)
	if err != nil {
		return nil, err
	}
	var changed []*store.TGChat
	for _, c := range list {
		if c.Kind != "channel" || c.Blocked || c.Collect == on {
			continue
		}
		if err := e.TGSetCollect(c.Account, c.ID, on); err != nil {
			return changed, err
		}
		changed = append(changed, c)
	}
	return changed, nil
}

// tgRowOf — строка справочника из чата Telegram. Отдельной функцией,
// потому что забытое здесь поле не видно ничем: признак «группа с
// топиками» так и не сохранялся, и топиков не появлялось вовсе.
func tgRowOf(account string, ch tg.Chat) *store.TGChat {
	return &store.TGChat{Account: account, ID: ch.ID, Title: ch.Title, Name: ch.Name, Kind: ch.Kind,
		Access: ch.Access, Forum: ch.Forum, CanPost: ch.CanPost, Class: tg.KindClass(ch.Kind)}
}

// TGSetCollect — собирать этот чат или нет.
func (e *Engine) TGSetCollect(account string, id int64, on bool) error {
	c, ok := e.store.TGChat(account, id)
	if !ok {
		return fmt.Errorf("чата нет в списке")
	}
	class := c.Class
	if class == "" {
		class = tg.KindClass(c.Kind)
	}
	return e.store.SetTGChatCollect(account, id, on, class)
}

// TGCloseChat — собеседника выключили в настройках: переписка не
// удаляется, а уходит в память сводкой. Разговор с человеком не выкидывают
// в корзину — его перестают вести.
func (e *Engine) TGCloseChat(account string, id int64) {
	c, ok := e.store.TGChat(account, id)
	if !ok {
		return
	}
	semaChat, err := e.tgChatFor(c)
	if err != nil || semaChat == 0 {
		return
	}
	// сводка идёт по всему дереву: разговор темы и его ветка «Сёма», где
	// как раз и лежит разговор с моделью
	e.endChatTree(semaChat)
	log.Printf("telegram: переписка «%s» закрыта, сводка ушла в память", c.Title)
}

// TGForget — забыть учётку: сессия стирается, собранное остаётся. Чаты
// не удаляются нарочно: переписку человек не выбрасывал, он отключил
// учётку — и, войдя снова тем же номером, увидит её на месте.
func (e *Engine) TGForget(phone string) error {
	acc, ok := e.store.TGAccount(normalNumber(phone))
	if !ok {
		return nil
	}
	e.tgStopListener(acc.Phone)
	_ = e.store.SetSetting(tgCodeHashKey(acc.Phone), "")
	log.Printf("telegram: учётка %s забыта, собранное осталось", acc.Phone)
	return e.store.DeleteTGAccount(acc.Phone)
}

// tgPrompt — с кем Сёма может переписываться в Telegram и что именно ей
// там позволено. Без этого блока на просьбу «начни диалог с Анной» она
// честно отвечает «не вижу, кто это»: список собеседников есть в базе, но
// в промпт не попадал.
func (e *Engine) tgPrompt() string {
	chats, err := e.store.TGChats("", false) // все учётки: собеседник может быть в любой
	if err != nil || len(chats) == 0 {
		return ""
	}
	several := len(e.TGAccounts()) > 1
	names := map[string]string{}
	for _, a := range e.TGAccounts() {
		names[a.Phone] = a.Name()
	}
	var canRead, canReply, canSend []string
	for _, c := range chats {
		if c.Blocked {
			continue
		}
		who := c.Title
		if c.Name != "" {
			who += " (@" + c.Name + ")"
		}
		if several {
			// учёток несколько, и один и тот же человек бывает в обеих:
			// без пометки модель назовёт собеседника, а Сёма напишет не с
			// того номера
			who += " [" + names[c.Account] + "]"
		}
		if c.Collect {
			canRead = append(canRead, who)
		}
		if c.Reply {
			canReply = append(canReply, who)
		}
		if c.Send {
			canSend = append(canSend, who)
		}
	}
	if len(canRead)+len(canReply)+len(canSend) == 0 {
		return "\nTelegram подключён, но ни одному собеседнику права не выданы: писать и читать некому. " +
			"Если просят написать в Telegram — скажи, что права выдаются в «Настройки → Telegram», у каждого чата отдельно.\n"
	}
	var b strings.Builder
	b.WriteString("\nTelegram. Права выдаёт человек, у каждого собеседника отдельно.\n")
	if len(canRead) > 0 {
		b.WriteString("Читаешь переписку: " + strings.Join(clipList(canRead, 20), ", ") + ".\n")
	}
	if len(canReply) > 0 {
		b.WriteString("Можешь отвечать, когда написали: " + strings.Join(clipList(canReply, 20), ", ") + ".\n")
	}
	if len(canSend) > 0 {
		b.WriteString("Можешь писать первой: " + strings.Join(clipList(canSend, 20), ", ") + ".\n")
	}
	b.WriteString("Кого нет в списках — тому писать нельзя, и придумывать адресата не надо: скажи, что права на этого собеседника не выданы.\n")
	return b.String()
}

// clipList — не больше n имён, дальше счётчик: промпт не резиновый.
func clipList(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	out := append([]string{}, list[:n]...)
	return append(out, fmt.Sprintf("и ещё %d", len(list)-n))
}

// TGFind — найти собеседника по тому, как его назвал человек: по имени,
// части имени или нику. Возвращает единственное совпадение: если их
// несколько, выбирать должен человек, а не Сёма.
// account — в какой учётке искать; пусто — во всех. Точное совпадение
// сразу в двух учётках выбором не считается: один и тот же человек в
// личной и рабочей — два разных разговора, и решает человек.
func (e *Engine) TGFind(account, who string) (*store.TGChat, error) {
	who = strings.TrimSpace(strings.TrimPrefix(who, "@"))
	if who == "" {
		return nil, fmt.Errorf("не сказано, кому писать")
	}
	chats, err := e.store.TGChats(account, false)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(who)
	var exact, found []*store.TGChat
	for _, c := range chats {
		if strings.ToLower(c.Title) == needle || strings.ToLower(c.Name) == needle {
			exact = append(exact, c)
			continue
		}
		if strings.Contains(strings.ToLower(c.Title), needle) || strings.Contains(strings.ToLower(c.Name), needle) {
			found = append(found, c)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		found = exact
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("собеседника «%s» нет в списке чатов", who)
	case 1:
		return found[0], nil
	}
	var names []string
	for _, c := range found {
		names = append(names, e.tgNameWithAccount(c))
	}
	return nil, fmt.Errorf("кого именно: %s", strings.Join(clipList(names, 5), ", "))
}

// tgNameWithAccount — «Митя (личная)»: когда учёток несколько, имени мало.
func (e *Engine) tgNameWithAccount(c *store.TGChat) string {
	if len(e.TGAccounts()) < 2 {
		return c.Title
	}
	if a, ok := e.store.TGAccount(c.Account); ok {
		return c.Title + " (" + a.Name() + ")"
	}
	return c.Title
}

// TGSend — отправить сообщение собеседнику. Проверяет права: чат
// заблокирован или без права писать — отказ, и это не ошибка связи, а
// граница, которую поставил человек.
func (e *Engine) TGSend(ctx context.Context, account string, chatID int64, topic int, text string) error {
	return e.tgSendChecked(ctx, account, chatID, topic, text, false)
}

// TGSendApproved — то же, но по кнопке человека: права «Отвечать» и
// «Писать первым» разрешают Сёме писать самой, а нажатие человека — это
// и есть разрешение, спрашивать его дважды незачем.
func (e *Engine) TGSendApproved(ctx context.Context, account string, chatID int64, topic int, text string) error {
	return e.tgSendChecked(ctx, account, chatID, topic, text, true)
}

// topic — тема группы с топиками: права берутся темы, сообщение уходит в
// неё же. У чатов без тем — 0.
func (e *Engine) tgSendChecked(ctx context.Context, account string, chatID int64, topic int, text string, byHuman bool) error {
	c, cl, err := e.tgCheckSend(account, chatID, topic, byHuman)
	if err != nil {
		return err
	}
	// адрес не всегда совпадает с чатом: ответ под постом уходит в
	// группу обсуждения, ответом на копию поста
	id, access, kind, addrTopic := e.tgAddr(c)
	if err := cl.Send(ctx, id, access, kind, addrTopic, text); err != nil {
		return err
	}
	log.Printf("telegram: отправлено в «%s» (%d знаков)", tgWho(c), len([]rune(text)))
	return nil
}

// tgPhotoCaption — сколько знаков Telegram принимает в подписи к фото.
const tgPhotoCaption = 1024

// TGSendPhoto — пост с картинкой: фото с подписью. Права те же, что у
// текста, и проверяет их та же дверь, а не копия проверок.
func (e *Engine) TGSendPhoto(ctx context.Context, account string, chatID int64, topic int, a *store.Attachment, caption string) error {
	if a == nil || !strings.HasPrefix(a.MIME, "image/") {
		return fmt.Errorf("к посту можно приложить только картинку")
	}
	if n := len([]rune(caption)); n > tgPhotoCaption {
		return fmt.Errorf("подпись к фото длиной %d знаков: Telegram принимает не больше %d — сократите пост", n, tgPhotoCaption)
	}
	c, cl, err := e.tgCheckSend(account, chatID, topic, false)
	if err != nil {
		return err
	}
	raw, err := e.FileBytes(a)
	if err != nil {
		return fmt.Errorf("картинку #%d не прочитать: %w", a.ID, err)
	}
	id, access, kind, addrTopic := e.tgAddr(c)
	if err := cl.SendFile(ctx, id, access, kind, addrTopic, a.Filename, a.MIME, raw, caption); err != nil {
		return err
	}
	log.Printf("telegram: пост с картинкой в «%s» (%d знаков)", tgWho(c), len([]rune(caption)))
	return nil
}

// TGSendLongWithImage — длинный пост с картинкой крупным превью над текстом.
// Права — та же дверь, что у текста и фото.
func (e *Engine) TGSendLongWithImage(ctx context.Context, account string, chatID int64, topic int, imageURL, text string) error {
	if n := len([]rune(text)); n > 4096 {
		return fmt.Errorf("пост длиной %d знаков: Telegram принимает не больше 4096", n)
	}
	c, cl, err := e.tgCheckSend(account, chatID, topic, false)
	if err != nil {
		return err
	}
	id, access, kind, addrTopic := e.tgAddr(c)
	if err := cl.SendWithImagePreview(ctx, id, access, kind, addrTopic, text, imageURL); err != nil {
		return err
	}
	log.Printf("telegram: длинный пост с картинкой в «%s» (%d знаков)", tgWho(c), len([]rune(text)))
	return nil
}

// tgCheckSend — одна дверь для всего, что уходит от вашего имени:
// чат есть, не заблокирован, писать разрешено, в канал — только с правом
// публиковать, у учётки включена отправка.
func (e *Engine) tgCheckSend(account string, chatID int64, topic int, byHuman bool) (*store.TGChat, *tg.Client, error) {
	c, ok := e.store.TGChat(account, chatID)
	if !ok {
		return nil, nil, fmt.Errorf("чата %d нет в учётке %s", chatID, account)
	}
	if c.Forum && topic <= 0 {
		topic = tg.GeneralTopic // без адреса Telegram кладёт в «Общий» — по его правам и решаем
	}
	c = e.tgEffective(c, topic)
	switch {
	case c.Blocked:
		return nil, nil, fmt.Errorf("чат «%s» заблокирован: Сёма его не трогает", c.Title)
	case !byHuman && !c.Admin && !c.Reply && !c.Send:
		return nil, nil, fmt.Errorf("в чат «%s» писать не разрешено — права выдаются в «Настройки → Telegram»", c.Title)
	}
	if err := e.tgCanReply(c); err != nil {
		return nil, nil, err
	}
	cl, acc, err := e.tgClientOf(c)
	if err != nil {
		return nil, nil, err
	}
	if !acc.CanSend {
		return nil, nil, fmt.Errorf("у учётки %s отправка от вашего имени выключена в настройках Telegram", acc.Name())
	}
	return c, cl, nil
}

// ListenTelegram — надзиратель за слушателями учёток. У каждой включённой
// учётки свой слушатель и своё соединение: MTProto — это сессия одного
// аккаунта, и «одно соединение на всех» невозможно в принципе.
//
// Падение одной учётки не роняет соседние: слушатель живёт в своей
// горутине, паника в нём ловится здесь же и видна в журнале с номером —
// иначе непонятно, какая именно учётка отвалилась.
func (e *Engine) ListenTelegram(ctx context.Context) {
	e.tgSupervisor(ctx, time.Minute, func(ctx context.Context, phone string) {
		e.listenAccount(ctx, phone)
	})
}

// tgSupervisor — раз в «шаг» сверяет список включённых учёток и
// поднимает слушателя на каждую, у которой его ещё нет; выключенную или
// забытую останавливает. Отдельной функцией с подставляемым слушателем,
// потому что проверять «упала одна — работает вторая» надо без сети.
func (e *Engine) tgSupervisor(ctx context.Context, step time.Duration, listen func(context.Context, string)) {
	for {
		if ctx.Err() != nil {
			e.tgStopAll()
			return
		}
		need := map[string]bool{}
		for _, a := range e.TGAccounts() {
			if a.Enabled && a.State == "ok" {
				need[a.Phone] = true
			}
		}
		e.tgLive.Lock()
		if e.tgListeners == nil {
			e.tgListeners = map[string]context.CancelFunc{}
		}
		for phone, stop := range e.tgListeners {
			if !need[phone] {
				stop()
				delete(e.tgListeners, phone)
				log.Printf("telegram: слушатель %s остановлен: учётка выключена или забыта", phone)
			}
		}
		var create []string
		for phone := range need {
			if _, has := e.tgListeners[phone]; !has {
				own, stop := context.WithCancel(ctx)
				e.tgListeners[phone] = stop
				create = append(create, phone)
				go func(phone string, ctx context.Context) {
					// паника в чужом протоколе не должна уносить остальные
					// учётки вместе с сервером
					defer func() {
						if r := recover(); r != nil {
							log.Printf("telegram: слушатель %s упал: %v", phone, r)
						}
						e.tgLive.Lock()
						if stop, has := e.tgListeners[phone]; has {
							stop()
							delete(e.tgListeners, phone)
						}
						e.tgLive.Unlock()
					}()
					listen(ctx, phone)
				}(phone, own)
			}
		}
		e.tgLive.Unlock()
		for _, phone := range create {
			log.Printf("telegram: поднимаю слушателя учётки %s", phone)
		}
		if !sleepCtx(ctx, step) {
			e.tgStopAll()
			return
		}
	}
}

// tgStopListener — учётку забыли или выключили: соединение рвём
// сразу, не дожидаясь обхода надзирателя.
func (e *Engine) tgStopListener(phone string) {
	e.tgLive.Lock()
	defer e.tgLive.Unlock()
	if stop, has := e.tgListeners[phone]; has {
		stop()
		delete(e.tgListeners, phone)
	}
}

func (e *Engine) tgStopAll() {
	e.tgLive.Lock()
	defer e.tgLive.Unlock()
	for phone, stop := range e.tgListeners {
		stop()
		delete(e.tgListeners, phone)
	}
}

// TGListeners — сколько учёток сейчас на связи (для экранов и тестов).
func (e *Engine) TGListeners() int {
	e.tgLive.Lock()
	defer e.tgLive.Unlock()
	return len(e.tgListeners)
}

// listenAccount — живое соединение одной учётки. MTProto присылает
// сообщения сам, поэтому опрашивать нечего: держим соединение и
// переподключаемся с растущей паузой, если оно рвётся.
func (e *Engine) listenAccount(ctx context.Context, phone string) {
	pause := pauseInitial
	link := "Telegram · " + phone
	for {
		if ctx.Err() != nil {
			return
		}
		cl, acc, err := e.tgClient(phone)
		if err != nil || acc.State != "ok" {
			// вход ещё не выполнен — ждём, это не ошибка
			if !sleepCtx(ctx, time.Minute) {
				return
			}
			continue
		}
		log.Printf("telegram: слушаю сообщения от имени %s", acc.Phone)
		e.LinkAlive(link)
		// выбранные чаты должны быть в Сёме сразу, не дожидаясь письма:
		// человек включил переписку — значит хочет её видеть
		go e.TGOpenAccount(ctx, acc.Phone)
		liveSince := time.Now()
		// по клиенту слушателя, пока его соединение живо, идёт разговор с
		// хозяином: «прочитано», «печатает», ответ по мере написания
		e.tgListenSet(acc.Phone, cl)
		err = cl.Listen(ctx, func(u tg.Update) { e.onTGMessage(acc.Phone, u) })
		e.tgListenSet(acc.Phone, nil)
		if ctx.Err() != nil {
			return
		}
		lived := time.Since(liveSince) // до сна: иначе в срок жизни попадёт пауза
		log.Printf("telegram: связь %s оборвалась через %s: %v (переподключусь через %s)",
			acc.Phone, lived.Round(time.Second), err, pause)
		e.Dropped(link, pause, pauseCap, shortErr(fmt.Sprint(err)))
		if !sleepCtx(ctx, pause) {
			return
		}
		pause = recountPause(pause, lived)
	}
}

// onTGMessage — что делать с новым сообщением. Права смотрим на каждое:
// человек мог закрыть чат минуту назад.
func (e *Engine) onTGMessage(account string, u tg.Update) {
	// комментарий под постом приходит в группу обсуждения, а
	// принадлежит каналу: где чей разговор, решает tgRoute. Учётка
	// известна от слушателя: номер чата сам по себе ничей
	if len(u.Deleted) > 0 {
		e.onTGDeleted(account, u)
		return
	}
	peer, topic, skip := e.tgRoute(account, u)
	if skip || peer == nil || !e.tgWatched(peer) {
		return // чат не выбран, закрыт или это копия поста в группе обсуждения
	}
	// решают права темы: у группы «читать» может быть выключено, а тема
	// открыта своим правом — и наоборот
	c := e.tgEffective(peer, topic)
	if c.Blocked || !c.Collect {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// хозяин увидит «прочитано» сразу, а не после того, как скачается
	// вложение и оживёт разговор: две галочки — первое, что он ждёт
	if c.Admin && !u.Out {
		e.tgTalkWith(c, true).markRead(u.MsgID)
	}

	// вложение: без него сообщение приходит пустым, и в переписке видно
	// «ничего» вместо «прислала фотографию». Номер файла в тексте — это
	// чип в пузыре: картинка показывается прямо в переписке. Голосовое
	// распознаётся (tg_voice.go): текст стоит вместо имени файла
	var voice *heardVoice
	var text string
	if u.File != nil && u.File.Voice && !u.Out {
		// пока идёт распознавание, хозяин видит «печатает»: он ждёт ответа
		stopTyping := func() {}
		if c.Admin && !c.Blocked {
			stopTyping = e.tgTalkWith(c, true).startTyping()
		}
		v := e.tgHearVoice(ctx, c, u)
		stopTyping()
		voice = &v
		text = v.line(u.Text)
	} else {
		text = e.tgTextWithFile(ctx, c, u, true)
	}
	// архив физический: чат, откуда сообщение пришло, и номер в нём —
	// иначе номера канала и группы обсуждения столкнулись бы в паре
	// «чат + номер», и комментарий затёр бы пост молча
	fresh, err := e.store.AddTGMessage(&store.TGMessage{
		Account: account, ChatID: u.ChatID, MsgID: u.MsgID, Topic: tgArchiveTopic(u, c, topic),
		Author: u.From, Text: text, Out: u.Out,
	})
	if err != nil {
		log.Printf("telegram: сообщение из «%s» не сохранено: %v", c.Title, err)
		return
	}
	// свой пост в своём канале — это содержимое канала, под ним живут
	// комментарии, и место в ленте ему нужно. Раньше исходящее отбрасывалось
	// целиком, а в канале, где публикует владелец, исходящее всё: 17
	// сентября 2026 пост Сёмы вышел в «Технический форум», а чат канала в
	// Сёме стоял пустым. Прочее исходящее по-прежнему только в архив
	ownPost := u.Out && c.Kind == "channel" && u.ChatID == c.ID
	if !fresh || (u.Out && !ownPost) {
		return
	}
	feed := text
	if c.Kind == "channel" && u.ChatID != c.ID {
		feed = e.tgCommentText(c, topic, text) // комментарий в ленте канала подписан постом
	}
	// разговор виден в Сёме отдельным чатом: там же лежит история, там же
	// человек может вмешаться и написать сам; у темы — свой, у канала
	// одна лента на посты и комментарии
	semaChat, err := e.tgConvFor(c, topic)
	if err != nil {
		log.Printf("telegram: чат для «%s» не заведён: %v", tgWho(c), err)
	} else {
		// «peer» — реплика собеседника: в переписке она слева и подписана
		// именем, а не выглядит так, будто это написали вы. В админ-чате
		// это вопрос хозяина — его положит ChatReply, второй пузырь не нужен
		if !(c.Admin && !c.Blocked) {
			_ = e.store.AddChatMsgExt(semaChat, "peer", u.From, feed, time.Time{}, tgMsgRef(account, u.ChatID, u.MsgID))
		}
		if c.Kind == "channel" && u.ChatID == c.ID {
			// где будут комментарии к посту, знает только Telegram:
			// спрашиваем отдельным заходом, входящее сети ждать не должно
			if _, ok := e.store.TGPostLink(c.Account, c.ID, u.MsgID); !ok {
				go e.tgLinkPost(e.tgEffective(c, 0), u.MsgID, semaChat)
			}
		}
	}
	// свой пост — не входящее: ни автоответа, ни уведомления о собственной
	// публикации
	if ownPost {
		return
	}
	if c.Admin && !c.Blocked && semaChat > 0 {
		// админ-чат: это не собеседник, это хозяин — сообщение идёт тем же
		// путём, что вопрос в чате Сёмы, и ответ уходит обратно сразу
		question := text
		if voice != nil {
			if voice.Err != nil {
				// Сёме говорить нечего: вопроса нет. Хозяин узнаёт об этом
				// сразу и словами, а не тишиной после «печатает»
				e.tgVoiceUnheard(ctx, c, semaChat, text, voice.Err)
				return
			}
			question = voiceMark + voice.Text
		}
		if err := e.tgAdminCommand(ctx, c, question, semaChat); err != nil {
			log.Printf("telegram: команда из «%s»: %v", c.Title, err)
		}
		return
	}
	// голосовое, которое не удалось распознать, — не вопрос: отвечать на
	// «голосовое, не распознано» Сёме нечего, а выдумывать содержание нельзя
	if c.Reply && !(voice != nil && voice.Err != nil) {
		if err := e.tgAutoReply(ctx, c, text, semaChat, u.MsgID); err != nil {
			log.Printf("telegram: автоответ «%s»: %v", c.Title, err)
		}
	}
	_, _ = e.Notify(&store.Notification{
		Key: "tg:" + tgRef(c), Source: "core", Level: "info",
		Title:   "Telegram · " + tgWho(c),
		Summary: clip(text, 200),
		Actions: tgNoticeActions(semaChat),
	})
}

// tgWatched — стоит ли вообще смотреть на чат: право группы или право
// хоть одной её темы. Иначе включённый тумблер темы при выключенной
// группе ничего не делал, а экран показывал «Сёма читает».
func (e *Engine) tgWatched(c *store.TGChat) bool {
	if c == nil || c.Blocked {
		return false
	}
	if c.Collect {
		return true
	}
	return c.Forum && e.store.TGTopicCollect(c.Account, c.ID)
}

// tgSaveFile — вложение из Telegram в файлы Сёмы. Источник виден в
// «Файлах», как у писем и папок: откуда файл — это часть файла.
func (e *Engine) tgSaveFile(ctx context.Context, c *store.TGChat, u tg.Update) (int64, error) {
	raw, err := e.fetchTG(ctx, c, u.File)
	if err != nil {
		return 0, err
	}
	if len(raw) == 0 {
		return 0, fmt.Errorf("пустой файл")
	}
	att, _, err := e.AddFile(ctx, u.File.Name, u.File.MIME, raw,
		FileSource{Kind: store.SourceTG, Ref: fmt.Sprintf("%s/%d/%d", c.Account, c.ID, u.MsgID), Account: c.Title}, nil)
	if err != nil || att == nil {
		return 0, err
	}
	log.Printf("telegram: файл «%s» из «%s» (%d КБ, #%d)", u.File.Name, c.Title, len(raw)/1024, att.ID)
	return att.ID, nil
}

// tgTextWithFile — текст сообщения вместе с вложением. Файл забирается,
// если у чата включены файлы: всегда у живого сообщения, а в истории —
// только картинки (all == false): тридцать фотографий ленты канала нужны
// на экране, а скачивать разом весь архив документов человек не просил.
func (e *Engine) tgTextWithFile(ctx context.Context, c *store.TGChat, u tg.Update, all bool) string {
	text := u.Text
	if u.File == nil {
		return text
	}
	if u.File.Voice {
		// историю и исходящие голосовые не расшифровываем: это деньги за
		// каждую запись, о которой никто не просил; живое входящее разбирает
		// onTGMessage
		return strings.TrimSpace(text + " " + tgHistoryVoice(u.File))
	}
	name := u.File.Name
	if c.Files && (all || strings.HasPrefix(u.File.MIME, "image/")) {
		id, err := e.tgSaveFile(ctx, c, u)
		switch {
		case err != nil:
			log.Printf("telegram: вложение из «%s» не забрали: %v", c.Title, err)
		case id > 0:
			return strings.TrimSpace(fmt.Sprintf("%s «%s» (#%d)", text, name, id))
		}
	}
	return strings.TrimSpace(text + " [" + name + "]")
}

// TGChatsMax — сколько диалогов читать из списка Telegram при обновлении:
// предохранитель, ведь Telegram отдаёт их по сотне, включая архив.
func (e *Engine) TGChatsMax() int { return e.store.SettingInt(tgChatsMaxDef) }

// TGFileLimitMB — предел вложения, которое Сёма забирает. Настройка, а
// не число в коде: у кого-то фотографии, у кого-то часовые видео.
func (e *Engine) TGFileLimitMB() int { return e.store.SettingInt(tgFileLimitDef) }

// sleepCtx — пауза, которую можно прервать остановкой сервера.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// tgAutoReply — ответ без кнопки. Право «отвечать» и есть разрешение
// человека: он выдал его этому собеседнику осознанно, как выдаёт сервер
// модулю. Инициатива при этом остаётся за собеседником — Сёма отвечает,
// а не начинает разговор.
func (e *Engine) tgAutoReply(ctx context.Context, c *store.TGChat, incoming string, semaChat int64, msgID int) error {
	acc, ok := e.store.TGAccount(c.Account)
	if !ok || !acc.CanSend {
		return nil // отправка выключена целиком — молчим, это не ошибка
	}
	if n := e.store.CountTGSentSince(c.Account, c.ID, c.Topic, time.Now().Add(-time.Hour).Format(time.RFC3339)); n >= e.TGReplyMaxHour() {
		// молчание должно быть объяснено: человек видит, что собеседник
		// пишет, а Сёма не отвечает, и не знает почему
		e.tellQuiet(c, semaChat, n)
		return fmt.Errorf("уже %d автоответов за час — молчу, чтобы не устроить переписку сама с собой", n)
	}
	// собеседник видит, что Сёма пишет ответ. Только здесь — после всех
	// отказов выше (отправка выключена, предел в час): молчащий Сёма «не
	// печатает». Ответ у чата с правом «Отвечать вместо меня» уходит без
	// кнопки, поэтому и статус честный; ждущего одобрения черновика сюда не
	// попадает — он идёт другим путём. «Прочитано» здесь не по умолчанию:
	// отметка снимает непрочитанное у владельца на телефоне, и включает её
	// отдельная настройка tg.read_mark_reply
	talk := e.tgTalkWith(c, false)
	talk.markReadReply(msgID)
	defer talk.startTyping()()
	// история читается там, где она лежит: у поста канала это ветка
	// комментариев в группе обсуждения, а не сам канал
	archChat, _, _, archTopic := e.tgAddr(c)
	history, _ := e.store.TGMessages(c.Account, archChat, archTopic, 10)
	var lines []string
	for i := len(history) - 1; i >= 0; i-- {
		who := c.Title
		if a := strings.TrimSpace(history[i].Author); a != "" {
			who = a // в группе пишут многие: имя автора важнее имени чата
		}
		if history[i].Out {
			who = "Вы"
		}
		lines = append(lines, who+": "+history[i].Text)
	}
	where := "Собеседник: " + c.Title + ".\n"
	switch {
	case c.Kind == "channel" && c.Topic > 0:
		where = "Канал «" + c.Title + "», пост «" + c.TopicTitle + "»: ответ уйдёт комментарием под этот пост.\n"
	case c.Topic > 0:
		where = "Группа «" + c.Title + "», тема «" + c.TopicTitle + "»: ответ уйдёт в эту тему.\n"
	}
	prompt := "Ты — Сёма, помощник владельца. Отвечаешь в Telegram от его имени.\n" +
		where +
		"Переписка:\n" + strings.Join(lines, "\n") + "\n" +
		"Новое сообщение: " + incoming + "\n\n" +
		"Ответь одним коротким сообщением по-русски, без приветствий на каждый раз и без выдумок. " +
		"Если не знаешь ответа или вопрос требует решения владельца — так и напиши, что передашь ему."
	answer, _, err := e.askRoleGuarded(llm.WithConsumer(ctx, "core:telegram"), RoleChat, prompt, true)
	if err != nil {
		return err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil
	}
	id, err := e.store.AddTGOutbox(&store.TGOutbox{Account: c.Account, ChatID: c.ID, Topic: c.Topic, SemaChat: semaChat, Who: tgWho(c), Text: answer})
	if err != nil {
		return err
	}
	// с живым соединением ответ уходит по нему и кладётся в архив сам;
	// без него — прежним путём, и архив пополнит эхо
	var sendErr error
	if talk != nil {
		sendErr = talk.newStream(0).finish(context.WithoutCancel(ctx), answer)
	} else {
		sendErr = e.TGSend(ctx, c.Account, c.ID, c.Topic, answer)
	}
	if sendErr != nil {
		_ = e.store.DecideTGOutbox(id, "rejected", sendErr.Error())
		return sendErr
	}
	_ = e.store.DecideTGOutbox(id, "sent", "автоответ")
	if semaChat > 0 {
		_ = e.store.AddChatMsg(semaChat, "sent", answer)
		// разбор — в тред: человек видит, что Сёма ответил сам и на что
		_ = e.saveToolCard(semaChat, ToolCard{Title: "Автоответ · " + tgWho(c), Code: answer,
			Result: "отправлено: у чата включено «Отвечать вместо меня»",
			TG:     &TGCard{ID: id, Account: c.Account, Chat: c.ID, Who: tgWho(c), Text: answer, Status: "sent"}})
	}
	log.Printf("telegram: автоответ в «%s»", tgWho(c))
	// человек должен видеть, что сказали от его имени
	_, _ = e.Notify(&store.Notification{
		Key: "tg-auto:" + tgRef(c), Source: "core", Level: "info",
		Title:   "Сёма ответил в Telegram · " + tgWho(c),
		Summary: clip(answer, 200),
		Fields: []store.NotifyField{
			{Label: "Ему написали", Value: clip(incoming, 300)},
			{Label: "Сёма ответил", Value: clip(answer, 300)},
		},
		Actions: []store.NotifyAction{{Label: "Настройки чата", Kind: "goto", Ref: "settings:telegram"}},
	})
	return nil
}

// tgAdminCommand — сообщение из админ-чата: как вопрос хозяина в чате
// Сёмы. Скрипты, команды и таймеры выполняются как обычно — изменяющее
// ждёт кнопки в «Уведомлениях»; текст ответа уходит в Telegram сразу.
//
// Пока идёт работа, хозяин видит то же, что в живом чате (tg_talk.go):
// «прочитано» (его ставит onTGMessage, ещё до вложений), «печатает» и ответ,
// который пишется на глазах. Слушателя нет — ничего из этого не
// запускается, и ответ уходит прежним путём.
func (e *Engine) tgAdminCommand(ctx context.Context, c *store.TGChat, incoming string, semaChat int64) error {
	talk := e.tgTalkWith(c, true)
	defer talk.startTyping()() // гасится по defer: статус не должен висеть после ошибки
	// пока прошлый ответ не ушёл, следующий ждёт: иначе оба делят одну ленту
	// и один набор частичного текста
	defer e.tgAdminLock(semaChat)()

	// команда хозяина — вопрос Сёме: разговор идёт в треде «Сёма», а в
	// ленте остаётся только то, что реально ушло хозяину в Telegram
	thread := e.chatForOutput(semaChat)
	stream := talk.newStream(thread)
	stopStream := stream.start()
	replyErr := e.ChatReply(ctx, thread, incoming)
	stopStream() // дождаться конца начатого обращения: итог кладётся только после него

	answer := ""
	switch {
	case replyErr != nil:
		// хозяин не должен гадать, почему молчат: ответ не собрался
		answer = "Не получилось ответить: " + clip(replyErr.Error(), 300)
	default:
		msgs, err := e.store.ChatMsgs(thread, 12)
		if err != nil {
			return err
		}
		answer = lastAnswerText(msgs)
	}
	shown := stream != nil && stream.msgID != 0
	if answer == "" && !shown {
		return nil // ответа текстом не было: только карточки
	}
	if answer == "" {
		answer = tgCardsOnly // черновик уже на экране хозяина: пусть кончится строкой, а не обрывком
	}
	id, err := e.store.AddTGOutbox(&store.TGOutbox{Account: c.Account, ChatID: c.ID, Topic: c.Topic, SemaChat: semaChat, Who: c.Title, Text: answer})
	if err != nil {
		return err
	}
	// итог кладётся со свежим сроком: тот, что дан ответу, мог выйти вместе
	// с моделью, а хозяину его всё равно доставить
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	var sendErr error
	if talk != nil {
		sendErr = stream.finish(fctx, answer)
	} else {
		sendErr = e.TGSendApproved(fctx, c.Account, c.ID, c.Topic, answer)
	}
	if sendErr != nil {
		_ = e.store.DecideTGOutbox(id, "rejected", sendErr.Error())
		return sendErr
	}
	_ = e.store.DecideTGOutbox(id, "sent", "админ-чат")
	if semaChat > 0 {
		_ = e.store.AddChatMsg(semaChat, "sent", answer) // видно, что ушло хозяину
	}
	return replyErr
}

// lastAnswerText — текст последнего ответа Сёмы: карточки команд ложатся
// после текста, поэтому «последняя реплика» — не всегда ответ. Идём от
// свежих к старым, карточки и заметки пропускаем, на вопросе человека
// останавливаемся.
func lastAnswerText(msgs []*store.ChatMsg) string {
	// ChatMsgs отдаёт в хронологическом порядке (от старых к новым, свежие
	// в конце), поэтому идём с конца среза
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		switch m.Role {
		case "assistant":
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m.Content), "Sema:"))
		case "user", "peer":
			return ""
		}
	}
	return ""
}

// TGReplyMaxHour — сколько автоответов в час на один чат. Настройка:
// переписка двух ботов на ночь глядя никому не нужна.
func (e *Engine) TGReplyMaxHour() int {
	// живой разговор — это десятки реплик в час: с прежней десяткой Сёма
	// замолкала посреди беседы. Предел остаётся защитой от переписки двух
	// ботов, а не ограничением для человека
	return e.store.SettingInt(tgReplyMaxHourDef)
}

// tgChatFor — чат Сёмы для переписки с этим собеседником. Один на
// собеседника: разговор не должен рассыпаться на десяток чатов, а
// заводиться заново после каждого сообщения — тем более.
func (e *Engine) tgChatFor(c *store.TGChat) (int64, error) {
	key := tgChatKey(c.Account, c.ID)
	if v := e.store.Setting(key, ""); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			if _, err := e.store.Chat(id); err == nil {
				return id, nil
			}
		}
	}
	id, err := e.store.CreateChat("Telegram · " + c.Title)
	if err != nil {
		return 0, err
	}
	if err := e.store.SetSetting(key, strconv.FormatInt(id, 10)); err != nil {
		return 0, err
	}
	log.Printf("telegram: завёл чат «%s» для переписки", c.Title)
	return id, nil
}

// Ключи адресов: номер чата сам по себе ничей, и без номера учётки чат 7
// личной и чат 7 рабочей смотрели бы в один разговор Сёмы.
func tgChatKey(account string, id int64) string {
	return fmt.Sprintf("tg.chat.%s.%d", account, id)
}

// tgChatKeyPrefix — с чего начинаются все такие ключи: по нему идёт
// обратный поиск «чат Сёмы → собеседник».
const tgChatKeyPrefix = "tg.chat."

// tgFromKeyChat — учётка и номер чата из ключа настройки.
func tgFromKeyChat(key string) (account string, id int64, ok bool) {
	rest := strings.TrimPrefix(key, tgChatKeyPrefix)
	i := strings.LastIndex(rest, ".")
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(rest[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return rest[:i], n, true
}

// tgHistoryKey — метка «историю этого разговора уже перенесли».
func tgHistoryKey(account string, id int64, topic int) string {
	if topic > 0 {
		return fmt.Sprintf("tg.history.%s.%d.%d", account, id, topic)
	}
	return fmt.Sprintf("tg.history.%s.%d", account, id)
}

// tgFeedKey — отметка «лента канала собрана». Пока её нет, история
// канала кладётся в ленту и тогда, когда архив её уже видел.
func tgFeedKey(c *store.TGChat) string {
	return fmt.Sprintf("tg.feed.v1.%s.%d", c.Account, c.ID)
}

// TGHistoryLimit — сколько прошлых сообщений переносить в чат Сёмы при
// открытии переписки. Настройка, а не число в коде: у одного собеседника
// хватит десятка, у другого нужен весь вчерашний разговор.
func (e *Engine) TGHistoryLimit() int { return e.store.SettingInt(tgHistoryDef) }

// TGOpenChat — завести чат Сёмы для собеседника прямо сейчас и перенести
// в него прошлую переписку. Человек включил чтение — разговор должен быть
// виден целиком, а не начинаться со следующего входящего сообщения.
// История переносится один раз (метка `tg.history.<чат>`), файлы из неё не
// качаются: скачивать разом весь архив вложений человек не просил.
func (e *Engine) TGOpenChat(ctx context.Context, account string, id int64) (int64, error) {
	c, ok := e.store.TGChat(account, id)
	if !ok {
		return 0, fmt.Errorf("чата нет в списке")
	}
	if c.Blocked || !c.Collect {
		return 0, nil // чат закрыт или не выбран — заводить нечего
	}
	semaChat, err := e.tgChatFor(c)
	if err != nil {
		return 0, err
	}
	if c.Forum {
		// группа с топиками: контейнер и разговор на каждую тему, история
		// тоже по темам — общая лента у такой группы не читается
		cl, acc, err := e.tgClientOf(c)
		if err != nil || acc.State != "ok" {
			return semaChat, fmt.Errorf("вход в Telegram не выполнен")
		}
		if err := e.tgRefreshTopics(ctx, cl, c); err != nil {
			log.Printf("telegram: темы «%s» не прочитаны: %v", c.Title, err)
		}
		topics, _ := e.store.TGTopics(c.Account, c.ID)
		for _, t := range topics {
			if ctx.Err() != nil {
				break
			}
			eff := e.tgEffective(c, t.ID)
			if eff.Blocked || !eff.Collect {
				continue
			}
			// разговор заводится под первое сообщение, а не под каждую
			// тему из списка: пустые темы не должны заполнять список чатов
			if err := e.tgOpenHistory(ctx, cl, eff, 0); err != nil {
				log.Printf("telegram: история «%s» не прочитана: %v", tgWho(eff), err)
			}
		}
		return semaChat, nil
	}
	if e.store.Setting(tgHistoryKey(c.Account, c.ID, 0), "") != "" {
		return semaChat, nil
	}
	cl, acc, err := e.tgClientOf(c)
	if err != nil || acc.State != "ok" {
		return semaChat, fmt.Errorf("вход в Telegram не выполнен")
	}
	return semaChat, e.tgOpenHistory(ctx, cl, c, semaChat)
}

// tgOpenHistory — прошлая переписка собеседника (или темы) в разговор
// Сёмы, один раз: метка tg.history.<чат> или tg.history.<чат>.<тема>.
// semaChat == 0 — разговор заводится здесь и только если есть что класть.
func (e *Engine) tgOpenHistory(ctx context.Context, cl *tg.Client, c *store.TGChat, semaChat int64) error {
	mark := tgHistoryKey(c.Account, c.ID, c.Topic)
	if e.store.Setting(mark, "") != "" {
		return nil
	}
	limit := e.TGHistoryLimit()
	if limit == 0 {
		return e.store.SetSetting(mark, "0")
	}
	// читаем там, где переписка лежит: у поста канала это ветка
	// комментариев в группе обсуждения
	addrID, addrAccess, addrKind, addrTopic := e.tgAddr(c)
	list, err := cl.History(ctx, addrID, addrAccess, addrKind, addrTopic, limit)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return e.store.SetSetting(mark, "0")
	}
	if semaChat == 0 {
		semaChat, err = e.tgConvFor(c, c.Topic)
		if err != nil {
			return err
		}
	}
	channel := c.Kind == "channel" && c.Topic <= 0 // лента постов, а не ветка комментариев
	var posts []int                                // свежие посты: к ним подтянем комментарии
	added := 0
	for _, u := range list {
		text := e.tgTextWithFile(ctx, c, u, false)
		if strings.TrimSpace(text) == "" {
			continue
		}
		arch := tgTopicOf(c, u)
		switch {
		case addrID != c.ID:
			arch = addrTopic // комментарии лежат под корнем ветки в группе обсуждения
		case channel:
			arch = u.MsgID // тема поста в архиве — его номер, как у живого поста
		}
		fresh, err := e.store.AddTGMessage(&store.TGMessage{
			Account: c.Account, ChatID: addrID, MsgID: u.MsgID, Topic: arch, Author: u.From, Text: text, Out: u.Out,
		})
		if err != nil {
			continue
		}
		// лента канала собирается заново и после переезда из разговоров
		// по постам: архив у таких постов уже есть, а в ленте их нет
		if !fresh && !(channel && e.store.Setting(tgFeedKey(c), "") == "") {
			continue
		}
		if addrID != c.ID && u.MsgID == addrTopic {
			continue // копия поста в группе обсуждения: сам пост уже в ленте
		}
		role := "peer" // реплика собеседника: слева, его именем
		if u.Out && c.Kind != "channel" {
			role = "user" // это писали вы: справа, как свои сообщения; пост канала — пост канала
		}
		feed := text
		if c.Kind == "channel" && c.Topic > 0 {
			feed = e.tgCommentText(c, c.Topic, text)
		}
		if err := e.store.AddChatMsgExt(semaChat, role, u.From, feed, u.Date, tgMsgRef(c.Account, addrID, u.MsgID)); err == nil {
			added++
		}
		if channel && !e.tgPostTooOld(u.Date) {
			posts = append(posts, u.MsgID)
		}
	}
	if channel {
		_ = e.store.SetSetting(tgFeedKey(c), "1")
		e.tgCommentsLater(c, posts, semaChat)
	}
	log.Printf("telegram: чат «%s» открыт, перенесено сообщений: %d", tgWho(c), added)
	return e.store.SetSetting(mark, strconv.Itoa(added))
}

// TGOpenAccount — то же для всех выбранных чатов одной учётки: после
// перезапуска и после первого входа переписки должны быть на месте, не
// дожидаясь сообщений.
func (e *Engine) TGOpenAccount(ctx context.Context, account string) {
	list, err := e.store.TGChats(account, true)
	if err != nil {
		log.Printf("telegram: список выбранных чатов %s не прочитан: %v", account, err)
		return
	}
	// разово на канал: разговоры по постам собираются обратно в одну
	// ленту, как в самом Telegram (tgChannelFeed)
	defer e.tgBindFeedRefs(account) // после истории: ленты уже собраны
	for _, c := range list {
		if c.Kind == "channel" && e.store.Setting(tgFeedKey(c), "") == "" {
			if err := e.tgChannelFeed(c); err != nil {
				log.Printf("telegram: канал «%s» не собран в ленту: %v", c.Title, err)
			}
		}
	}
	for _, c := range list {
		if ctx.Err() != nil {
			return
		}
		if _, err := e.TGOpenChat(ctx, c.Account, c.ID); err != nil {
			log.Printf("telegram: чат «%s» не открыт: %v", c.Title, err)
		}
		if c.Kind == "channel" && !c.Blocked && c.Collect {
			e.tgCommentsMissing(c)
		}
	}
}

// TGOpenAll — то же по всем учёткам.
func (e *Engine) TGOpenAll(ctx context.Context) {
	for _, a := range e.TGAccounts() {
		if ctx.Err() != nil {
			return
		}
		e.TGOpenAccount(ctx, a.Phone)
	}
}

// tgNoticeActions — куда ведёт запись о новом сообщении: в саму
// переписку, а не в настройки. Разбираться с ней обычно не нужно —
// достаточно прочитать, поэтому «Прочитано» добавляет экран сам.
func tgNoticeActions(semaChat int64) []store.NotifyAction {
	if semaChat > 0 {
		return []store.NotifyAction{
			{Label: "Открыть переписку", Kind: "goto", Ref: "chat:" + strconv.FormatInt(semaChat, 10)},
		}
	}
	return []store.NotifyAction{{Label: "Настройки чата", Kind: "goto", Ref: "settings:telegram"}}
}

// tellQuiet — сказать, почему Сёма замолчал: предел автоответов в час.
// Раз в час на чат, иначе на каждое сообщение будет по записи.
func (e *Engine) tellQuiet(c *store.TGChat, semaChat int64, n int) {
	key := "tg.quiet." + tgRef(c) // в tgRef уже стоит номер учётки
	if last := parseTimeSafe(e.store.Setting(key, "")); time.Since(last) < time.Hour {
		return
	}
	_ = e.store.SetSetting(key, time.Now().Format(time.RFC3339))
	note := fmt.Sprintf("Молчу: за час уже %d автоответов в этом чате, предел — %d (настройка tg.reply_max_hour). "+
		"Ответьте сами или поднимите предел.", n, e.TGReplyMaxHour())
	if semaChat > 0 {
		_ = e.store.AddChatMsg(e.chatForOutput(semaChat), "note", note)
	}
	_, _ = e.Notify(&store.Notification{
		Key: "tg-quiet:" + tgRef(c), Source: "core", Level: "warn",
		Title:   "Сёма замолчал · " + tgWho(c),
		Summary: note,
		Actions: []store.NotifyAction{
			{Label: "Открыть переписку", Kind: "goto", Ref: "chat:" + strconv.FormatInt(semaChat, 10)},
		},
	})
}

// parseTimeSafe — время из строки RFC3339; непонятная строка — нулевое
// время, а не ошибка: так «ни разу не было» и «запись испорчена» читаются
// одинаково, а решать по ним всё равно нечего.
func parseTimeSafe(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return v
}

package engine

// MAX как источник — вторым каналом после Telegram и по тем же правилам:
// вход от вашего имени, права по каждому собеседнику, переписка видна
// отдельным чатом Сёмы, написанное человеком уходит адресату сразу.
// Токен доступа лежит в базе шифрованным и наружу не выдаётся.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/max"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// maxVerifyKey — токен подтверждения между «запросили код» и «ввели код».
// maxCaptchaKey — ссылка на проверку VK, пока она нужна.
const (
	maxVerifyKey  = "max.verify_token"
	maxCaptchaKey = "max.captcha_link"
)

// MAXCaptchaLink — ссылка на проверку VK для экрана; пусто — не нужна.
func (e *Engine) MAXCaptchaLink() string { return e.store.Setting(maxCaptchaKey, "") }

// maxClient — клиент с сохранённым удостоверением. Второе значение —
// сам аккаунт: экраны спрашивают его состояние.
func (e *Engine) maxClient() (*max.Client, *store.MAXAccount, error) {
	acc, ok := e.store.MAXAccountGet()
	if !ok {
		return nil, nil, fmt.Errorf("аккаунт MAX не заведён")
	}
	if acc.DeviceID == "" {
		acc.DeviceID = newDeviceID()
		_ = e.store.SaveMAXAccount(acc)
	}
	cl := max.New(acc.DeviceID, acc.Token)
	cl.AllowExpiredCert(e.MAXAllowExpired())
	return cl, acc, nil
}

// MAXAllowExpired — принимать узлы MAX с просроченным сертификатом.
// Настройка, а не флаг в коде: это решение человека, и он его видит.
func (e *Engine) MAXAllowExpired() bool {
	return e.store.Setting("max.allow_expired_cert", "") == "1"
}

// SetMAXAllowExpired — тумблер на экране «Настройки → MAX».
func (e *Engine) SetMAXAllowExpired(on bool) error {
	v := ""
	if on {
		v = "1"
	}
	return e.store.SetSetting("max.allow_expired_cert", v)
}

// newDeviceID — удостоверение устройства. Постоянное: со сменой MAX
// просит вход заново, а частая смена выглядит как чужой вход.
func newDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// MAXStartLogin — первый шаг входа: код на телефон.
func (e *Engine) MAXStartLogin(ctx context.Context, phone, captcha string) error {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return fmt.Errorf("нужен номер телефона")
	}
	acc, ok := e.store.MAXAccountGet()
	if !ok {
		acc = &store.MAXAccount{Phone: phone, DeviceID: newDeviceID()}
	}
	acc.Phone, acc.State, acc.Note = phone, "code", "ждём код из SMS"
	if acc.DeviceID == "" {
		acc.DeviceID = newDeviceID()
	}
	if err := e.store.SaveMAXAccount(acc); err != nil {
		return err
	}
	cl := max.New(acc.DeviceID, acc.Token)
	cl.AllowExpiredCert(e.MAXAllowExpired())
	defer cl.Close()
	token, err := cl.AskCode(ctx, phone, captcha)
	if err != nil {
		// «Проверка не пройдена» — это проверка VK: спрашиваем ссылку и
		// показываем её человеку, сама Сёма капчу не проходит
		if link, cerr := cl.Captcha(ctx, phone); cerr == nil && link != "" {
			_ = e.store.SetSetting(maxCaptchaKey, link)
			acc.State = "captcha"
			acc.Note = "MAX просит проверку VK: откройте ссылку, пройдите её и вставьте код проверки"
			_ = e.store.SaveMAXAccount(acc)
			return fmt.Errorf("MAX просит проверку VK — она открывается по ссылке на экране")
		}
		acc.State, acc.Note = "fail", err.Error()
		_ = e.store.SaveMAXAccount(acc)
		return err
	}
	_ = e.store.SetSetting(maxCaptchaKey, "")
	return e.store.SetSetting(maxVerifyKey, token)
}

// MAXConfirmCode — второй шаг: код из SMS в обмен на токен доступа.
func (e *Engine) MAXConfirmCode(ctx context.Context, code string) error {
	acc, ok := e.store.MAXAccountGet()
	if !ok {
		return fmt.Errorf("сначала введите номер телефона")
	}
	verify := e.store.Setting(maxVerifyKey, "")
	if verify == "" {
		return fmt.Errorf("код устарел — запросите новый")
	}
	cl := max.New(acc.DeviceID, "")
	cl.AllowExpiredCert(e.MAXAllowExpired())
	defer cl.Close()
	token, err := cl.SubmitCode(ctx, verify, code)
	if err != nil {
		acc.State, acc.Note = "fail", err.Error()
		_ = e.store.SaveMAXAccount(acc)
		return err
	}
	acc.Token, acc.State, acc.Note = token, "ok", "вход выполнен"
	acc.Last = time.Now().Format(time.RFC3339)
	if err := e.store.SaveMAXAccount(acc); err != nil {
		return err
	}
	_ = e.store.SetSetting(maxVerifyKey, "")
	log.Printf("max: вход выполнен от имени %s", acc.Phone)
	return nil
}

// MAXSetToken — токен доступа, взятый в браузере (web.max.ru): обходной
// путь, когда проверка VK не отдаёт токен наружу. Токен ложится в базу
// зашифрованным, как и полученный по SMS.
func (e *Engine) MAXSetToken(ctx context.Context, phone, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("нужен токен доступа")
	}
	acc, ok := e.store.MAXAccountGet()
	if !ok {
		acc = &store.MAXAccount{Phone: strings.TrimSpace(phone), DeviceID: newDeviceID()}
	}
	if p := strings.TrimSpace(phone); p != "" {
		acc.Phone = p
	}
	if acc.DeviceID == "" {
		acc.DeviceID = newDeviceID()
	}
	// проверяем сразу: токен без входа — это молчание, а не связь
	cl := max.New(acc.DeviceID, token)
	cl.AllowExpiredCert(e.MAXAllowExpired())
	defer cl.Close()
	fromProfile, err := cl.Login(ctx)
	if err != nil {
		acc.State, acc.Note = "fail", "токен не подошёл: "+err.Error()
		if acc.Phone != "" {
			_ = e.store.SaveMAXAccount(acc)
		}
		return err
	}
	if acc.Phone == "" {
		acc.Phone = fromProfile // вход из браузера: номер берём из профиля
	}
	if acc.Phone == "" {
		return fmt.Errorf("MAX не назвал номер — впишите телефон в форму")
	}
	acc.Token, acc.State, acc.Note = token, "ok", "вход по токену из браузера"
	acc.Last = time.Now().Format(time.RFC3339)
	if err := e.store.SaveMAXAccount(acc); err != nil {
		return err
	}
	_ = e.store.SetSetting(maxCaptchaKey, "")
	_ = e.store.SetSetting(maxVerifyKey, "")
	log.Printf("max: вход по токену от имени %s", acc.Phone)
	return nil
}

// MAXForget — забыть аккаунт: токен стирается, собранное остаётся.
func (e *Engine) MAXForget() error {
	acc, ok := e.store.MAXAccountGet()
	if !ok {
		return nil
	}
	_ = e.store.SetSetting(maxVerifyKey, "")
	return e.store.DeleteMAXAccount(acc.Phone)
}

// MAXRefreshChats — обновить список чатов. Выбор человека не затирается.
func (e *Engine) MAXRefreshChats(ctx context.Context) (int, error) {
	cl, acc, err := e.maxClient()
	if err != nil {
		return 0, err
	}
	defer cl.Close()
	if _, err := cl.Login(ctx); err != nil {
		return 0, err
	}
	list, err := cl.Chats(ctx, 100)
	if err != nil {
		return 0, err
	}
	for _, c := range list {
		row := &store.MAXChat{ID: c.ID, Title: c.Title, Kind: c.Kind, Class: maxClass(c.Kind)}
		if have, ok := e.store.MAXChat(c.ID); ok {
			row.Collect, row.Send, row.Files, row.Blocked = have.Collect, have.Send, have.Files, have.Blocked
			row.Class, row.Count = have.Class, have.Count
		}
		if err := e.store.SaveMAXChat(row); err != nil {
			return 0, err
		}
	}
	acc.Last = time.Now().Format(time.RFC3339)
	acc.Note = fmt.Sprintf("чатов в списке: %d", len(list))
	_ = e.store.SaveMAXAccount(acc)
	return len(list), nil
}

// maxClass — класс чувствительности по умолчанию: личная переписка и
// группы чувствительны, канал — обычное чтение.
func maxClass(kind string) string {
	if kind == "channel" {
		return "обычное"
	}
	return "чувствительное"
}

// MAXSetCollect — «читать переписку» с этим собеседником.
func (e *Engine) MAXSetCollect(id int64, on bool) error {
	c, ok := e.store.MAXChat(id)
	if !ok {
		return fmt.Errorf("чата нет в списке")
	}
	class := c.Class
	if class == "" {
		class = maxClass(c.Kind)
	}
	return e.store.SetMAXChatCollect(id, on, class)
}

// ListenMAX — живое соединение: MAX присылает сообщения сам. Обрыв —
// переподключение с растущей паузой, как в Telegram.
func (e *Engine) ListenMAX(ctx context.Context) {
	pause := pauseInitial
	for {
		if ctx.Err() != nil {
			return
		}
		cl, acc, err := e.maxClient()
		if err != nil || acc.State != "ok" {
			if !sleepCtx(ctx, time.Minute) { // вход ещё не выполнен — ждём
				return
			}
			continue
		}
		log.Printf("max: слушаю сообщения от имени %s", acc.Phone)
		e.LinkAlive("MAX")
		go e.MAXOpenAll(ctx)
		liveSince := time.Now()
		err = cl.Listen(ctx, func(u max.Update) { e.onMAXMessage(u) })
		cl.Close()
		if ctx.Err() != nil {
			return
		}
		// сколько соединение прожило — считаем ДО сна: после него в это
		// число попала бы сама пауза, и пятиминутный сон объявлял бы
		// любое соединение пожившим, обнуляя весь отсчёт
		lived := time.Since(liveSince)
		log.Printf("max: связь оборвалась через %s: %v (переподключусь через %s)",
			lived.Round(time.Second), err, pause)
		// наблюдателю говорим то же самое, что журналу: сто двадцать
		// одинаковых строк за ночь человек не прочтёт, а состояние
		// «пауза стоит в потолке» он увидит
		e.Dropped("MAX", pause, pauseCap, shortErr(fmt.Sprint(err)))
		if !sleepCtx(ctx, pause) {
			return
		}
		pause = recountPause(pause, lived)
	}
}

// onMAXMessage — новое сообщение. Права смотрим на каждое: человек мог
// закрыть чат минуту назад.
func (e *Engine) onMAXMessage(u max.Update) {
	c, ok := e.store.MAXChat(u.ChatID)
	if !ok || c.Blocked || !c.Collect {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	text := e.maxWithFiles(ctx, c, u, c.Files, true)
	fresh, err := e.store.AddMAXMessage(&store.MAXMessage{
		ChatID: u.ChatID, MsgID: u.MsgID, Text: text, Out: u.Out,
	})
	if err != nil {
		log.Printf("max: сообщение из «%s» не сохранено: %v", c.Title, err)
		return
	}
	if !fresh || u.Out {
		return
	}
	semaChat, err := e.maxChatFor(c)
	if err != nil {
		log.Printf("max: чат для «%s» не заведён: %v", c.Title, err)
		return
	}
	// «peer» — реплика собеседника: слева и подписана его именем
	_ = e.store.AddChatMsg(semaChat, "peer", text)
	_, _ = e.Notify(&store.Notification{
		Key: fmt.Sprintf("max:%d", c.ID), Source: "core", Level: "info",
		Title:   "MAX · " + c.Title,
		Summary: clip(text, 200),
		Actions: []store.NotifyAction{
			{Label: "Открыть переписку", Kind: "goto", Ref: "chat:" + strconv.FormatInt(semaChat, 10)},
		},
	})
}

// maxChatFor — чат Сёмы для переписки с этим собеседником. Один на
// собеседника, как в Telegram.
func (e *Engine) maxChatFor(c *store.MAXChat) (int64, error) {
	key := fmt.Sprintf("max.chat.%d", c.ID)
	if v := e.store.Setting(key, ""); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			if _, err := e.store.Chat(id); err == nil {
				return id, nil
			}
		}
	}
	id, err := e.store.CreateChat("MAX · " + c.Title)
	if err != nil {
		return 0, err
	}
	if err := e.store.SetSetting(key, strconv.FormatInt(id, 10)); err != nil {
		return 0, err
	}
	log.Printf("max: завёл чат «%s» для переписки", c.Title)
	return id, nil
}

// MAXPeerOfChat — собеседник, к которому привязан чат Сёмы.
func (e *Engine) MAXPeerOfChat(semaChat int64) (*store.MAXChat, bool) {
	want := strconv.FormatInt(semaChat, 10)
	for key, v := range e.store.SettingsByPrefix("max.chat.") {
		if v != want {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(key, "max.chat."), 10, 64)
		if err != nil {
			continue
		}
		return e.store.MAXChat(id)
	}
	return nil, false
}

// MAXOpenChat — завести чат Сёмы прямо сейчас и перенести прошлую
// переписку. История переносится один раз, метка `max.history.<чат>`.
func (e *Engine) MAXOpenChat(ctx context.Context, id int64) (int64, error) {
	c, ok := e.store.MAXChat(id)
	if !ok {
		return 0, fmt.Errorf("чата нет в списке")
	}
	if c.Blocked || !c.Collect {
		return 0, nil
	}
	semaChat, err := e.maxChatFor(c)
	if err != nil {
		return 0, err
	}
	mark := fmt.Sprintf("max.history.%d", c.ID)
	if e.store.Setting(mark, "") != "" {
		return semaChat, nil
	}
	limit := e.TGHistoryLimit() // предел общий для каналов: правило одно
	if limit == 0 {
		return semaChat, e.store.SetSetting(mark, "0")
	}
	cl, acc, err := e.maxClient()
	if err != nil || acc.State != "ok" {
		return semaChat, fmt.Errorf("вход в MAX не выполнен")
	}
	defer cl.Close()
	if _, err := cl.Login(ctx); err != nil {
		return semaChat, err
	}
	list, err := cl.History(ctx, c.ID, limit)
	if err != nil {
		return semaChat, err
	}
	added := 0
	for _, u := range list {
		text := e.maxWithFiles(ctx, c, u, false, false) // историю не качаем и голоса в ней не расшифровываем
		if strings.TrimSpace(text) == "" {
			continue
		}
		fresh, err := e.store.AddMAXMessage(&store.MAXMessage{
			ChatID: c.ID, MsgID: u.MsgID, Text: text, Out: u.Out,
		})
		if err != nil || !fresh {
			continue
		}
		role := "peer"
		if u.Out {
			role = "user"
		}
		if err := e.store.AddChatMsg(semaChat, role, text); err == nil {
			added++
		}
	}
	log.Printf("max: чат «%s» открыт, перенесено сообщений: %d", c.Title, added)
	return semaChat, e.store.SetSetting(mark, strconv.Itoa(added))
}

// MAXOpenAll — то же для всех выбранных чатов: после перезапуска и после
// первого входа переписки должны быть на месте.
func (e *Engine) MAXOpenAll(ctx context.Context) {
	list, err := e.store.MAXChats(true)
	if err != nil {
		return
	}
	for _, c := range list {
		if ctx.Err() != nil {
			return
		}
		if _, err := e.MAXOpenChat(ctx, c.ID); err != nil {
			log.Printf("max: чат «%s» не открыт: %v", c.Title, err)
		}
	}
}

// maxSendByHuman — сообщение, которое человек написал сам, уходит
// собеседнику сразу: права чата спрашивают, можно ли писать самой Сёме.
func (e *Engine) maxSendByHuman(semaChat int64, c *store.MAXChat, text string) error {
	if c.Blocked {
		return fmt.Errorf("чат «%s» заблокирован: Сёма его не трогает", c.Title)
	}
	cl, acc, err := e.maxClient()
	if err != nil {
		return err
	}
	defer cl.Close()
	if !acc.CanSend {
		return fmt.Errorf("отправка от вашего имени выключена — включите её в «Настройки → MAX»")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := cl.Login(ctx); err != nil {
		return err
	}
	if err := cl.Send(ctx, c.ID, text); err != nil {
		return err
	}
	log.Printf("max: человек написал в «%s» (%d знаков)", c.Title, len([]rune(text)))
	return e.store.AddChatMsg(semaChat, "user", text)
}

// maxWithFiles — текст сообщения вместе с вложениями. Файл, который
// разрешено забирать, ложится в «Файлы» Сёмы, а его номер попадает в
// текст: по номеру переписка показывает картинку прямо в пузыре. Нельзя
// забирать — остаётся имя, чтобы не было «ничего» вместо фотографии.
//
// live — сообщение пришло сейчас, а не из истории. Только живое голосовое
// расшифровывается: каждая запись стоит денег, и разбирать разом всю
// историю чата, о которой никто не просил, нельзя.
func (e *Engine) maxWithFiles(ctx context.Context, c *store.MAXChat, u max.Update, take, live bool) string {
	text := strings.TrimSpace(u.Text)
	for _, a := range u.Files {
		if a.Voice() && live && !u.Out {
			if line, ok := e.maxHearVoice(ctx, c, u, a, take); ok {
				text = strings.TrimSpace(text + " " + line)
				continue
			}
		}
		if take && a.URL != "" {
			id, err := e.maxSaveFile(ctx, c, u, a)
			if err != nil {
				log.Printf("max: вложение из «%s» не забрали: %v", c.Title, err)
			} else if id > 0 {
				text = strings.TrimSpace(fmt.Sprintf("%s «%s» (#%d)", text, a.Human(), id))
				continue
			}
		}
		text = strings.TrimSpace(text + " [" + a.Human() + "]")
	}
	return text
}

// maxHearVoice — расшифровать живое голосовое MAX. ok=false — расшифровывать
// нечего (у вложения нет адреса): вызывающий подписывает его как раньше.
//
// В MAX нет админ-чата, и любой голос здесь чужой: облачной модели он уходит
// только при `speech.outside` = all, а доверенной — всегда. Не вышло — та же
// строка «голосовое, не распознано: причина», что в Telegram.
func (e *Engine) maxHearVoice(ctx context.Context, c *store.MAXChat, u max.Update, a max.Attach, keepFile bool) (string, bool) {
	if a.URL == "" {
		// адреса нет — забирать нечего; форму кадра (имена полей, не
		// значения) пишем в журнал: по ней видно, где искать
		log.Printf("max: голосовое из «%s» без адреса вложения: вид %s, поля %v", c.Title, a.Kind, a.Keys)
		return "", false
	}
	v := heardVoice{Name: a.Human()}
	raw, mime, err := e.maxDownload(ctx, a.URL)
	if err != nil {
		v.Err = fmt.Errorf("запись не скачалась: %w", err)
		return v.line(""), true
	}
	if strings.EqualFold(a.Kind, "UNSUPPORTED") && !strings.HasPrefix(mime, "audio/") {
		// вид MAX не назвал, а по типу это не запись: звуком не считаем и
		// провайдеру не отправляем
		v.Err = fmt.Errorf("вложение не звук (%s)", mime)
		return v.line(""), true
	}
	name := "voice-" + strconv.FormatInt(u.MsgID, 10) + ".ogg"
	if a.Name != "" {
		name = a.Name
	}
	amime := audioMIME(name, mime)
	text, model, err := e.Hear(ctx, VoiceIn{Audio: raw, MIME: amime, Own: false, Consumer: "core:max"})
	if err != nil {
		v.Err = err
	} else {
		v.Text = text
	}
	if keepFile {
		var known *knownText
		if v.Err == nil {
			known = &knownText{Text: text, Via: "распознавание речи · " + model}
		}
		att, _, ferr := e.addFile(ctx, name, amime, raw,
			FileSource{Kind: store.SourceMAX, Ref: fmt.Sprintf("%d/%d", c.ID, u.MsgID), Account: c.Title}, nil, known)
		if ferr != nil {
			log.Printf("max: голосовое из «%s» не легло в файлы: %v", c.Title, ferr)
		} else if att != nil {
			v.FileID, v.Name = att.ID, name
		}
	}
	return v.line(""), true
}

// maxDownload — вложение MAX по адресу, подписанному самим MAX (запрос
// обычный, без ключей). Предел размера общий для каналов.
func (e *Engine) maxDownload(ctx context.Context, url string) (raw []byte, mime string, err error) {
	limit := int64(e.TGFileLimitMB()) << 20
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("MAX отдал файл с ответом %s", resp.Status)
	}
	raw, err = io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("пустой файл")
	}
	return raw, resp.Header.Get("Content-Type"), nil
}

// maxSaveFile — вложение MAX в файлы Сёмы. Адрес подписан самим MAX,
// поэтому забирается обычным запросом, без ключей.
func (e *Engine) maxSaveFile(ctx context.Context, c *store.MAXChat, u max.Update, a max.Attach) (int64, error) {
	raw, mime, err := e.maxDownload(ctx, a.URL)
	if err != nil {
		return 0, err
	}
	name := a.Human()
	att, _, err := e.AddFile(ctx, name, mime, raw,
		FileSource{Kind: store.SourceMAX, Ref: fmt.Sprintf("%d/%d", c.ID, u.MsgID), Account: c.Title}, nil)
	if err != nil || att == nil {
		return 0, err
	}
	log.Printf("max: файл «%s» из «%s» (%d КБ, #%d)", name, c.Title, len(raw)/1024, att.ID)
	return att.ID, nil
}

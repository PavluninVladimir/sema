package engine

// Разговор в Telegram вживую. Хозяин пишет Сёме в Telegram и ждёт того же,
// что в любом чате: сразу видеть, что сообщение прочитано, потом «печатает…»,
// а ответ — появляющимся на глазах, а не одним куском через минуту.
//
//   - «прочитано» ставится, как только входящее принято (`tg.read_mark`); в
//     чатах, где Сёма отвечает сам, — только по отдельному `tg.read_mark_reply`
//     (по умолчанию нет: отметка снимает непрочитанное у владельца на телефоне);
//   - «печатает» повторяется, пока идёт вся работа над ответом — модель,
//     скрипты, команды, второй ход, — и гаснет по defer, что бы ни случилось
//     (`tg.typing`); Telegram снимает статус сам секунд через пять, отсюда ритм
//     в четыре;
//   - ответ отправляется, как только у модели есть первые слова, и дальше
//     правится (`tg.stream` = edit) не чаще `tg.stream_ms`; вариант draft —
//     живой черновик Telegram, off — одним куском, как раньше;
//   - всё это идёт по соединению слушателя (tg.Client.use): своё соединение
//     на каждое действие — это секунды рукопожатия там, где человек смотрит
//     на экран. Слушателя нет — ничего из перечисленного не запускается, и
//     ответ уходит прежним путём.
//
// Черновые правки в журнал отправленного не пишутся: в `tg_outbox` и в чат
// Сёмы итог ложится один раз, когда ответ ушёл целиком.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// tgTransport — то, что разговору нужно от Telegram. *tg.Client умеет всё
// это по живому соединению; в тестах вместо него Engine.tgVia.
type tgTransport interface {
	SendText(ctx context.Context, p tg.Peer, text string) (int, error)
	EditText(ctx context.Context, p tg.Peer, msgID int, text string) error
	Typing(ctx context.Context, p tg.Peer, on bool) error
	LiveDraft(ctx context.Context, p tg.Peer, draftID int64, text string) error
	MarkRead(ctx context.Context, p tg.Peer, maxID int) error
}

var _ tgTransport = (*tg.Client)(nil)

// Виды потока ответа: настройка `tg.stream`.
const (
	tgStreamEdit  = "edit"  // сообщение отправляется на первых словах и правится
	tgStreamDraft = "draft" // живой черновик Telegram, итог — обычным сообщением
	tgStreamOff   = "off"   // одним куском, когда ответ готов
)

const (
	// tgTypingEvery — как часто повторять «печатает»: Telegram гасит статус
	// секунд через пять. Это свойство протокола, а не вкус человека, поэтому
	// не настройка.
	tgTypingEvery = 4 * time.Second
	// tgStreamFloor — быстрее раз в сто миллисекунд править нельзя, что бы
	// ни написали в настройке: ниже начинается FLOOD_WAIT.
	tgStreamFloor = 100 * time.Millisecond
	// tgCardsOnly — что видит хозяин, когда ответа текстом не вышло, а
	// черновик в Telegram уже показан: молчание после «пишет…» хуже строки.
	tgCardsOnly = "Ответ — в карточках в Сёме, текстом сказать нечего."
)

// tgFlag — тумблер настройки: по умолчанию включён, выключает только «0».
func (e *Engine) tgFlag(key string) bool { return e.tgFlagOr(key, true) }

// tgFlagOr — тумблер с заданным умолчанием: у выключенного по умолчанию
// включает только «1».
func (e *Engine) tgFlagOr(key string, def bool) bool {
	v := strings.TrimSpace(e.store.Setting(key, ""))
	if v == "" {
		return def
	}
	return v != "0"
}

// tgStreamMode — как ответ доезжает до хозяина (`tg.stream`).
func (e *Engine) tgStreamMode() string {
	switch v := strings.ToLower(strings.TrimSpace(e.store.Setting("tg.stream", tgStreamEdit))); v {
	case tgStreamDraft, tgStreamOff:
		return v
	}
	return tgStreamEdit
}

// tgStreamEvery — не чаще, чем раз в столько правится сообщение (`tg.stream_ms`).
func (e *Engine) tgStreamEvery() time.Duration {
	ms := e.store.SettingInt(tgStreamMsDef)
	d := time.Duration(ms) * time.Millisecond
	if d < tgStreamFloor {
		d = tgStreamFloor
	}
	return d
}

// tgStreamFirst — сколько знаков должно набраться, прежде чем ответ
// отправится первым сообщением (`tg.stream_first`): «Хо» в уведомлении на
// телефоне — плохое первое впечатление.
func (e *Engine) tgStreamFirst() int { return e.store.SettingInt(tgStreamFirstDef) }

func (e *Engine) tgPulseEvery() time.Duration {
	if e.tgPulse > 0 {
		return e.tgPulse
	}
	return tgTypingEvery
}

// ---- «прочитано» и «печатает» ----

// tgTalk — разговор с одним чатом: куда обращаться и чем.
type tgTalk struct {
	e    *Engine
	c    *store.TGChat
	tr   tgTransport
	peer tg.Peer
	key  string // ритм «печатает» один на чат
}

// tgTalkWith открывает разговор вживую. nil — нельзя или не на чем: права
// (та же дверь, что у любой отправки), нет соединения слушателя. Все методы
// разговора живут и на nil и ничего не делают: тогда ответ уходит прежним
// путём, а не молчанием.
func (e *Engine) tgTalkWith(c *store.TGChat, byHuman bool) *tgTalk {
	_, cl, err := e.tgCheckSend(c.Account, c.ID, c.Topic, byHuman)
	if err != nil {
		return nil
	}
	var tr tgTransport = cl
	switch {
	case e.tgVia != nil:
		tr = e.tgVia
	default:
		live := e.tgListenClient(c.Account)
		if live == nil {
			return nil // без соединения слушателя каждое действие — своё рукопожатие
		}
		tr = live
	}
	id, access, kind, topic := e.tgAddr(c)
	return &tgTalk{e: e, c: c, tr: tr, peer: tg.Peer{ID: id, Access: access, Kind: kind, Topic: topic},
		key: fmt.Sprintf("%s:%d/%d", c.Account, id, topic)}
}

// tgListenClient — клиент слушателя этой учётки, пока его соединение живо.
func (e *Engine) tgListenClient(phone string) *tg.Client {
	e.tgLive.Lock()
	cl := e.tgListenCl[phone]
	e.tgLive.Unlock()
	if cl != nil && cl.Alive() {
		return cl
	}
	return nil
}

func (e *Engine) tgListenSet(phone string, cl *tg.Client) {
	e.tgLive.Lock()
	defer e.tgLive.Unlock()
	if e.tgListenCl == nil {
		e.tgListenCl = map[string]*tg.Client{}
	}
	if cl == nil {
		delete(e.tgListenCl, phone)
		return
	}
	e.tgListenCl[phone] = cl
}

// recoverTalk — украшение разговора не должно ронять сервер: паника в
// фоновой горутине «печатает» или потока правок остаётся строкой в журнале,
// а сам ответ доезжает итогом.
func recoverTalk(what string) {
	if r := recover(); r != nil {
		log.Printf("telegram: %s упало: %v", what, r)
	}
}

// tgCallCtx — срок одного обращения: разговор вживую не ждёт минутами.
func tgCallCtx(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// markRead — «прочитано» в разговоре с хозяином (`tg.read_mark`, по умолчанию
// включено).
func (t *tgTalk) markRead(msgID int) { t.markReadBy(msgID, "tg.read_mark", true) }

// markReadReply — «прочитано» в чате, где Сёма отвечает сам
// (`tg.read_mark_reply`, по умолчанию выключено): отметка сама снимает
// непрочитанное у владельца на телефоне, а живость он просил только для
// своего разговора с Сёмой.
func (t *tgTalk) markReadReply(msgID int) { t.markReadBy(msgID, "tg.read_mark_reply", false) }

// markReadBy — «прочитано» до этого сообщения, если включён этот тумблер. В
// теме форума не ставится: у темы своя отметка, а общая прочла бы и все
// соседние.
func (t *tgTalk) markReadBy(msgID int, key string, def bool) {
	if t == nil || msgID <= 0 || t.peer.Topic > tg.GeneralTopic || !t.e.tgFlagOr(key, def) {
		return
	}
	ctx, cancel := tgCallCtx(15 * time.Second)
	defer cancel()
	if err := t.tr.MarkRead(ctx, t.peer, msgID); err != nil {
		log.Printf("telegram: «прочитано» в «%s» не поставлено: %v", tgWho(t.c), err)
	}
}

// tgPulse — «печатает» одного чата. Ответов может ждать несколько, а ритм
// один: считаем, сколько нуждаются в статусе, и гасим, когда не нужен никому.
type tgPulse struct {
	refs  int
	stop  chan struct{}
	done  chan struct{}
	quiet atomic.Int64 // до какого момента (UnixNano) статус не подаётся: FLOOD_WAIT
}

func (p *tgPulse) hush(d time.Duration) {
	until := time.Now().Add(d).UnixNano()
	for {
		old := p.quiet.Load()
		if old >= until || p.quiet.CompareAndSwap(old, until) {
			return
		}
	}
}

// startTyping включает «печатает» и возвращает выключатель. Гасить его надо
// через defer: статус не должен висеть, если работа упала или её прервали.
func (t *tgTalk) startTyping() (stop func()) {
	if t == nil || !t.e.tgFlag("tg.typing") {
		return func() {}
	}
	e := t.e
	e.tgTalkMu.Lock()
	if e.tgPulses == nil {
		e.tgPulses = map[string]*tgPulse{}
	}
	p := e.tgPulses[t.key]
	fresh := p == nil
	if fresh {
		p = &tgPulse{stop: make(chan struct{}), done: make(chan struct{})}
		e.tgPulses[t.key] = p
	}
	p.refs++
	e.tgTalkMu.Unlock()
	if fresh {
		// первый статус — сразу и до всего остального, следующие — по ритму
		warned := t.beat(p, false)
		go t.pulse(p, warned)
	}
	var once sync.Once
	return func() { once.Do(func() { t.stopTyping(p) }) }
}

// beat — один «печатает». Возвращает, писали ли уже об ошибке в журнал:
// ритм длится минутами, а строка про одну и ту же беду нужна одна.
func (t *tgTalk) beat(p *tgPulse, warned bool) bool {
	if time.Now().UnixNano() < p.quiet.Load() {
		return warned
	}
	ctx, cancel := tgCallCtx(10 * time.Second)
	defer cancel()
	err := t.tr.Typing(ctx, t.peer, true)
	if err == nil {
		return warned
	}
	if d, ok := tg.FloodWait(err); ok {
		p.hush(d)
	} else if !warned {
		log.Printf("telegram: «печатает» в «%s» не подано: %v", tgWho(t.c), err)
		return true
	}
	return warned
}

func (t *tgTalk) pulse(p *tgPulse, warned bool) {
	defer close(p.done)
	defer recoverTalk("печатает")
	tick := time.NewTicker(t.e.tgPulseEvery())
	defer tick.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-tick.C:
			warned = t.beat(p, warned)
		}
	}
}

func (t *tgTalk) stopTyping(p *tgPulse) {
	e := t.e
	e.tgTalkMu.Lock()
	p.refs--
	last := p.refs <= 0
	if last && e.tgPulses[t.key] == p {
		delete(e.tgPulses, t.key)
	}
	e.tgTalkMu.Unlock()
	if !last {
		return
	}
	close(p.stop)
	<-p.done // после этого «печатает» уже не подастся
	// пока гасили, на этот же чат мог встать новый ответ: снимать его статус нельзя
	e.tgTalkMu.Lock()
	taken := e.tgPulses[t.key] != nil
	e.tgTalkMu.Unlock()
	if taken {
		return
	}
	ctx, cancel := tgCallCtx(10 * time.Second)
	defer cancel()
	if err := t.tr.Typing(ctx, t.peer, false); err != nil {
		log.Printf("telegram: «печатает» в «%s» не погашено: %v", tgWho(t.c), err)
	}
}

// tgAdminLock — ответы хозяину идут по очереди. Пишет он быстрее, чем Сёма
// отвечает; два ответа разом делят одну ленту чата и один набор частичного
// текста, и из них выходила бы каша.
func (e *Engine) tgAdminLock(chat int64) func() {
	e.tgTalkMu.Lock()
	if e.tgAdminLocks == nil {
		e.tgAdminLocks = map[int64]*sync.Mutex{}
	}
	mu := e.tgAdminLocks[chat]
	if mu == nil {
		mu = &sync.Mutex{}
		e.tgAdminLocks[chat] = mu
	}
	e.tgTalkMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// ---- ответ на глазах ----

// tgStream — один ответ, доезжающий до хозяина. Работает в две фазы: пока
// модель пишет, run по таймеру смотрит на частичный текст и отправляет или
// правит сообщение; когда ответ готов, finish кладёт итог — правкой того же
// сообщения или, если ничего не показывали, обычной отправкой.
//
// Поля run и finish не делят замка: finish зовётся, когда run уже остановлен
// и дождался конца своего обращения.
type tgStream struct {
	t     *tgTalk
	chat  int64 // чат Сёмы (тред), где пишется ответ
	mode  string
	every time.Duration
	first int

	msgID   int       // сообщение с показанным текстом; 0 — ничего не отправлено
	shown   string    // текст в нём сейчас
	ids     []int     // всё, что мы отправили в этом ответе: для архива
	next    time.Time // не раньше: частота правок и срок FLOOD_WAIT
	fails   int       // обращений подряд неудачно
	draftID int64
	draftOK bool // живой черновик принят, и мы ему верим
	stopped chan struct{}
	done    chan struct{}
}

func (t *tgTalk) newStream(chat int64) *tgStream {
	if t == nil {
		return nil
	}
	e := t.e
	s := &tgStream{t: t, chat: chat, mode: e.tgStreamMode(), every: e.tgStreamEvery(), first: e.tgStreamFirst()}
	if s.mode == tgStreamDraft {
		// аккаунт, которому Telegram уже отказал в черновике, помнится:
		// каждый раз пробовать заново — значит каждый раз показывать пустоту
		if e.store.Setting("tg.draft_off."+t.c.Account, "") == "" {
			s.draftOK = true
			s.draftID = time.Now().UnixNano()
		}
	}
	return s
}

// start запускает наблюдение за частичным текстом и возвращает остановку:
// она дожидается конца начатого обращения, поэтому после неё можно
// спокойно вызывать finish.
func (s *tgStream) start() (stop func()) {
	if s == nil || s.mode == tgStreamOff {
		return func() {}
	}
	s.stopped, s.done = make(chan struct{}), make(chan struct{})
	go s.run()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(s.stopped)
			<-s.done
		})
	}
}

func (s *tgStream) run() {
	defer close(s.done)
	defer recoverTalk("ответ на глазах")
	tick := s.every / 4
	if tick < 20*time.Millisecond {
		tick = 20 * time.Millisecond
	}
	if tick > 100*time.Millisecond {
		tick = 100 * time.Millisecond // первое слово ответа ждёт не дольше такта
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-s.stopped:
			return
		case <-t.C:
		}
		s.step(s.t.e.answerVisible(s.chat))
	}
}

// step — один взгляд на частичный текст. Обращения к Telegram не отменяются
// вместе с остановкой: оборванная отправка — это «сообщение, возможно,
// ушло», а повторять её значило бы получить два.
func (s *tgStream) step(vis string) {
	if vis == "" || time.Now().Before(s.next) || s.fails >= 3 {
		return
	}
	vis = strings.TrimSpace(tg.FirstChunk(vis))
	// то, что уже показано, не пересылаем; и не сжимаем показанное до его
	// начала: так бывает, когда на конце мелькнуло начало ограждения
	if vis == "" || vis == s.shown || (s.shown != "" && strings.HasPrefix(s.shown, vis)) {
		return
	}
	if s.mode == tgStreamDraft && s.draftOK {
		s.stepLiveDraft(vis)
		return
	}
	if s.msgID == 0 {
		if utf8.RuneCountInString(vis) < s.first {
			return
		}
		ctx, cancel := tgCallCtx(20 * time.Second)
		defer cancel()
		id, err := s.t.tr.SendText(ctx, s.t.peer, vis)
		if err != nil {
			s.failed("отправка", err)
			return
		}
		s.fails = 0
		s.msgID, s.shown, s.next = id, vis, time.Now().Add(s.every)
		s.ids = append(s.ids, id)
		s.t.archive(id, vis)
		return
	}
	// ход сменился (после команд модель пишет заново): пока новый текст короче
	// порога и не продолжает показанный, прежний остаётся на месте — иначе
	// сообщение сожмётся до двух слов и снова разрастётся
	if !strings.HasPrefix(vis, s.shown) && utf8.RuneCountInString(vis) < s.first {
		return
	}
	ctx, cancel := tgCallCtx(20 * time.Second)
	defer cancel()
	if err := s.t.tr.EditText(ctx, s.t.peer, s.msgID, vis); err != nil {
		if tg.Gone(err) {
			s.msgID, s.shown = 0, "" // удалили, пока писали: дальше новое сообщение
			return
		}
		s.failed("правка", err)
		return
	}
	s.fails = 0
	s.shown, s.next = vis, time.Now().Add(s.every)
}

// failed разбирает отказ по ходу: FLOOD_WAIT — это «подожди», а не «нельзя»,
// остальное считается, и после трёх подряд поток сдаётся до итога.
func (s *tgStream) failed(what string, err error) {
	if d, ok := tg.FloodWait(err); ok {
		s.next = time.Now().Add(d)
		return
	}
	s.fails++
	s.next = time.Now().Add(s.every)
	log.Printf("telegram: %s ответа в «%s» не прошла (%d): %v", what, tgWho(s.t.c), s.fails, err)
}

// stepLiveDraft — живой черновик. Первый же отказ Telegram (для аккаунта
// человека это «только для ботов») запоминается на этой учётке и в журнал
// пишется один раз; остаток ответа идёт правками, как в режиме edit.
func (s *tgStream) stepLiveDraft(vis string) {
	e := s.t.e
	if utf8.RuneCountInString(vis) < s.first {
		return
	}
	ctx, cancel := tgCallCtx(15 * time.Second)
	defer cancel()
	err := s.t.tr.LiveDraft(ctx, s.t.peer, s.draftID, vis)
	switch {
	case err == nil:
		s.fails = 0
		s.shown, s.next = vis, time.Now().Add(s.every)
	case isFlood(err):
		s.failed("черновик", err)
	case tg.Answered(err):
		reason := clip(err.Error(), 120)
		_ = e.store.SetSetting("tg.draft_off."+s.t.c.Account, reason)
		log.Printf("telegram: аккаунт %s не принимает живой черновик (%s) — отвечаю правками сообщения, дальше не пробую", s.t.c.Account, reason)
		s.draftOK = false
		s.shown = "" // показанного нет: первое сообщение уйдёт на следующем шаге
	default:
		s.failed("черновик", err)
	}
}

func isFlood(err error) bool { _, ok := tg.FloodWait(err); return ok }

// archive — своё сообщение в архив. Отправленное по соединению слушателя
// эхом ему не возвращается, и, если не положить его сюда, история для
// автоответа и поиск не увидят слов Сёмы.
func (t *tgTalk) archive(id int, text string) {
	topic := t.peer.Topic
	if topic <= 0 && t.c.Forum {
		topic = tg.GeneralTopic
	}
	_, _ = t.e.store.AddTGMessage(&store.TGMessage{Account: t.c.Account, ChatID: t.peer.ID, MsgID: id,
		Topic: topic, Text: text, Out: true})
}

// finish кладёт итог: правкой показанного сообщения (его первая часть) и
// новыми сообщениями для остального — Telegram принимает не больше 4096
// знаков. Ничего не показывали — просто отправка по частям. FLOOD_WAIT
// здесь пережидается, а не роняет ответ: итог обязан дойти.
func (s *tgStream) finish(ctx context.Context, final string) error {
	chunks := tg.SplitText(final, tg.MessageLimit)
	if len(chunks) == 0 {
		return nil
	}
	start := 0
	if s.msgID != 0 {
		err := s.editUntil(ctx, chunks[0])
		switch {
		case err == nil:
			start = 1
		case tg.Gone(err):
			s.msgID = 0 // сообщение пропало: итог уйдёт новым
		default:
			return err
		}
	}
	for _, part := range chunks[start:] {
		id, err := s.sendUntil(ctx, part)
		if err != nil {
			return err
		}
		s.ids = append(s.ids, id)
		s.t.archive(id, part)
	}
	return nil
}

// setArchived — в архиве остаётся итог, а не первые слова.
func (t *tgTalk) setArchived(id int, text string) {
	if err := t.e.store.SetTGMessageText(t.c.Account, t.peer.ID, id, text); err != nil {
		log.Printf("telegram: итог сообщения %d не лёг в архив: %v", id, err)
	}
}

// editUntil правит показанное сообщение, пережидая FLOOD_WAIT.
func (s *tgStream) editUntil(ctx context.Context, text string) error {
	if text == s.shown {
		s.t.setArchived(s.msgID, text)
		return nil
	}
	// итоговая правка подчиняется тому же ритму, что и остальные: последняя
	// не должна идти впритык к предыдущей и наступать на FLOOD_WAIT
	if d := time.Until(s.next); d > 0 && !sleepCtx(ctx, d) {
		return ctx.Err()
	}
	for {
		err := s.t.tr.EditText(ctx, s.t.peer, s.msgID, text)
		if err == nil {
			s.shown = text
			s.t.setArchived(s.msgID, text)
			return nil
		}
		d, ok := tg.FloodWait(err)
		if !ok || !sleepCtx(ctx, d) {
			return err
		}
	}
}

// sendUntil отправляет сообщение, пережидая FLOOD_WAIT.
func (s *tgStream) sendUntil(ctx context.Context, text string) (int, error) {
	for {
		id, err := s.t.tr.SendText(ctx, s.t.peer, text)
		if err == nil {
			return id, nil
		}
		d, ok := tg.FloodWait(err)
		if !ok || !sleepCtx(ctx, d) {
			return 0, err
		}
	}
}

// ---- что из частичного ответа можно показывать ----

// fencePrefix — с чего начинаются служебные ограждения Сёмы: ```sema:,
// ```sema-ssh:, ```sema-module и остальные. Их разбирает ядро, а хозяину
// показывать сырой код скрипта и команды нельзя.
const fencePrefix = "```sema"

// answerVisible — частичный ответ модели в том виде, в каком его можно
// показать снаружи: без метки говорящего, без текста прежнего хода (после
// команд модель пишет ответ заново) и обрезанный на начале первого
// служебного ограждения. Дальше ограждения текст не идёт вовсе, пока не
// придёт итог, в котором блоки уже разобраны.
func (e *Engine) answerVisible(chatID int64) string {
	e.thinkingMu.Lock()
	partial, skip := e.partial[chatID], e.partialSkip[chatID]
	e.thinkingMu.Unlock()
	return visiblePartial(partial, skip)
}

func visiblePartial(partial string, skip int) string {
	if skip > 0 {
		if skip >= len(partial) {
			return ""
		}
		partial = partial[skip:]
	}
	if i := strings.Index(partial, fencePrefix); i >= 0 {
		partial = partial[:i]
	}
	// ограждение может доезжать по кусочкам: «``» и «```se» на конце — уже
	// его начало, и мелькнуть в чате оно не должно
	for k := len(fencePrefix) - 1; k > 0; k-- {
		if strings.HasSuffix(partial, fencePrefix[:k]) {
			partial = partial[:len(partial)-k]
			break
		}
	}
	return trimSpeakerPrefix(partial)
}

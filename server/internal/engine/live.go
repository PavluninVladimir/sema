package engine

// Живое обновление: узел сигналов «экран устарел». Хранилище и движок
// говорят, какие экраны изменились; подписчики (соединения веб-клиента)
// получают имена экранов и перечитывают их обычной дорогой. По проводу
// не ходит ни разметка, ни данные — только имена.
//
// Сигналы склеиваются: у каждого подписчика своё множество грязных
// экранов и тикер. Пока модель пишет ответ, частичный текст меняется
// десятки раз в секунду — клиент получит не больше кадра за тик, и имя
// экрана в кадре один раз. Отправка идёт в горутине подписчика, движок
// в неё не ждёт: медленный телефон не задерживает ни модель, ни почту.

import (
	"context"
	"sort"
	"sync"
	"time"
)

// liveHub — подписчики и их грязные множества.
type liveHub struct {
	mu   sync.Mutex
	subs map[*liveSub]struct{}
}

type liveSub struct {
	mu    sync.Mutex
	dirty map[string]struct{}
	wake  chan struct{}
}

func newLiveHub() *liveHub { return &liveHub{subs: map[*liveSub]struct{}{}} }

// changed — экраны изменились. Не блокирует: только помечает.
func (h *liveHub) changed(screens ...string) {
	if h == nil || len(screens) == 0 {
		return
	}
	h.mu.Lock()
	subs := make([]*liveSub, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		s.mu.Lock()
		for _, sc := range screens {
			s.dirty[sc] = struct{}{}
		}
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default: // уже разбужен — сигнал не теряется, множество общее
		}
	}
}

// Subscribe — подписка на кадры. Кадр — отсортированный список имён
// экранов, изменившихся с прошлого кадра; не чаще одного за окно. Отписка
// — по контексту.
func (h *liveHub) subscribe(ctx context.Context, window time.Duration) <-chan []string {
	s := &liveSub{dirty: map[string]struct{}{}, wake: make(chan struct{}, 1)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	out := make(chan []string, 1)
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.subs, s)
			h.mu.Unlock()
			close(out)
		}()
		if window <= 0 {
			window = 300 * time.Millisecond
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			// окно склейки: что накопится за это время — уйдёт одним кадром
			select {
			case <-ctx.Done():
				return
			case <-time.After(window):
			}
			s.mu.Lock()
			frame := make([]string, 0, len(s.dirty))
			for sc := range s.dirty {
				frame = append(frame, sc)
			}
			s.dirty = map[string]struct{}{}
			s.mu.Unlock()
			if len(frame) == 0 {
				continue
			}
			sort.Strings(frame)
			// подписчик не вычитывает — кадр ждёт его в буфере, а следующие
			// копятся в множестве; никто не блокируется, ничего не теряется
			select {
			case out <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// Настройки живого обновления. Все — в базе, с экраном правки («Работа
// Сёмы» → «Экран и связь»); умолчания и границы — в реестре настроек
// (`setting_defs.go`), здесь только ключи.
const (
	settingLiveMS       = "ui.live_ms"        // окно склейки, мс
	settingPollS        = "ui.poll_s"         // опрос без провода, с
	settingPollLiveS    = "ui.poll_live_s"    // опрос при живом проводе, с
	settingWSWriteS     = "ui.ws_write_s"     // сколько ждать, пока клиент вычитает кадр, с
	settingWSPerSession = "ui.ws_per_session" // соединений на одну сессию
	settingSnapshotDays = "ui.snapshot_days"  // сколько дней живёт снимок экрана на устройстве
)

// LiveSettings — что нужно ручке и клиенту.
type LiveSettings struct {
	Window     time.Duration
	PollS      int
	PollLiveS  int
	WriteWait  time.Duration
	PerSession int
	// SnapshotDays — срок жизни снимка экрана на устройстве. Он не
	// показывается устаревшим, а стирается: снимок недельной давности
	// человек ещё узнаёт своим, месячный уже вводит в заблуждение.
	SnapshotDays int
}

func (e *Engine) LiveSettings() LiveSettings {
	st := e.store
	return LiveSettings{
		Window:       time.Duration(st.SettingInt(liveWindowDef)) * time.Millisecond,
		PollS:        st.SettingInt(livePollDef),
		PollLiveS:    st.SettingInt(livePollWiredDef),
		WriteWait:    time.Duration(st.SettingInt(liveWriteDef)) * time.Second,
		PerSession:   st.SettingInt(livePerSessionDef),
		SnapshotDays: st.SettingInt(snapshotDaysDef),
	}
}

// Changed — экраны изменились: зовут места, которые меняют то, что не
// доходит до хранилища (частичный ответ, признак «думает»).
func (e *Engine) Changed(screens ...string) { e.live.changed(screens...) }

// SubscribeLive — подписка для ручки.
func (e *Engine) SubscribeLive(ctx context.Context) <-chan []string {
	return e.live.subscribe(ctx, e.LiveSettings().Window)
}

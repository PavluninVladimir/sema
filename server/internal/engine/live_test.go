package engine

import (
	"context"
	"testing"
	"time"
)

// Сто изменений за окно — один кадр, имя экрана в нём один раз. Пока
// модель пишет ответ, частичный текст меняется десятки раз в секунду:
// клиент не должен получать по кадру на каждую букву.
func TestSignalsMerged(t *testing.T) {
	h := newLiveHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := h.subscribe(ctx, 50*time.Millisecond)
	for i := 0; i < 100; i++ {
		h.changed("chat:7")
		h.changed("chat:7", "notifications")
	}
	select {
	case f := <-frames:
		if len(f) != 2 || f[0] != "chat:7" || f[1] != "notifications" {
			t.Fatalf("кадр: %v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("кадра не дождались")
	}
	select {
	case f := <-frames:
		t.Fatalf("второй кадр без новых изменений: %v", f)
	case <-time.After(150 * time.Millisecond):
	}
}

// Подписчик, который не вычитывает, не задерживает движок: тысяча
// сигналов уходит за мгновение, а не ждёт медленный телефон.
func TestSlowSubscriberNotSlows(t *testing.T) {
	h := newLiveHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = h.subscribe(ctx, 10*time.Millisecond) // и никто не читает
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		h.changed("inbox")
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("сигналы ждали подписчика: %s", d)
	}
}

// Отписка по контексту: подписчик уходит, и его больше не считают.
func TestUnsubscribeByContext(t *testing.T) {
	h := newLiveHub()
	ctx, cancel := context.WithCancel(context.Background())
	frames := h.subscribe(ctx, 10*time.Millisecond)
	cancel()
	if _, ok := <-frames; ok {
		t.Fatal("канал не закрылся после отписки")
	}
	h.mu.Lock()
	n := len(h.subs)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("подписчик остался в узле: %d", n)
	}
}

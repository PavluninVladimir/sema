package engine

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Пауза растёт до потолка и там останавливается.
func TestPauseGrowsToCap(t *testing.T) {
	p := pauseInitial
	short := 10 * time.Second // соединение умерло сразу — беда тянется
	waited := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second,
		80 * time.Second, 160 * time.Second, 5 * time.Minute, 5 * time.Minute}
	for i, must := range waited {
		p = recountPause(p, short)
		if p != must {
			t.Fatalf("шаг %d: пауза %s, а должна быть %s", i+1, p, must)
		}
	}
}

// Пожившее соединение обнуляет паузу — иначе она меряет всю историю.
//
// Это ровно тот случай, что был у MAX 12 сентября: связь держалась по две
// минуты, а Сёма переподключался раз в пять минут двадцать секунд, потому
// что пауза дошла до потолка неделю назад и не сбрасывалась.
func TestLivedConnResetsPause(t *testing.T) {
	if p := recountPause(5*time.Minute, 2*time.Minute); p != pauseInitial {
		t.Fatalf("после рабочего соединения пауза %s, а должна вернуться к %s", p, pauseInitial)
	}
	// на границе не обнуляем: ровно минута это не «пожило»
	if p := recountPause(40*time.Second, livedEnough); p == pauseInitial {
		t.Fatal("соединение ровно на пороге сочли рабочим")
	}
}

// Правило одно на все соединения: свой отсчёт в цикле — это и есть та
// ошибка, которую здесь чинили.
func TestNoOwnPauseCount(t *testing.T) {
	for _, file := range []string{"max.go", "telegram.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if !strings.Contains(text, "recountPause(pause,") {
			t.Errorf("%s считает паузу сам, а не общим правилом", file)
		}
		if strings.Contains(text, "pause *= 2") {
			t.Errorf("%s удваивает паузу у себя — правило разъедется", file)
		}
	}
}

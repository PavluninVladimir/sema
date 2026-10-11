package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Тревога на «Здоровье» отделяется от телеметрии. До 10 сентября 2026
// «наружу ушло 514 отправок, из них 247 чувствительных» отличалось от
// «температура 47 °C» волоском янтарём и словом «внимание» десятым
// кеглем — то есть ничем.
func TestHealthAlertReadsAsOnePhrase(t *testing.T) {
	к := healthAlert(engine.HealthCard{
		Key: "sent", Title: "Ушло наружу · сутки", Value: "514 отправок",
		Meta: "чувствительных 247 · 189.1 тыс. токенов · всего вызовов 580", Level: "warn"})
	// имя показателя и его значение — одна фраза: врозь они читаются
	// как две разные записи
	if !strings.Contains(к.Subtitle, "Ушло наружу") || !strings.Contains(к.Subtitle, "514 отправок") {
		t.Errorf("тревога не называет ни что, ни сколько: %q", к.Subtitle)
	}
	if strings.Contains(к.Subtitle, "· сутки") {
		t.Errorf("служебный хвост остался в тревоге: %q", к.Subtitle)
	}
	if к.Caption == "" {
		t.Error("подробности потеряны")
	}
	if len(к.Badges) != 1 || к.Badges[0].Text == "" {
		t.Errorf("уровень не назван: %v", к.Badges)
	}
	// тревога без места, где с ней разобраться, — просто испуг
	if len(к.Actions) != 1 || к.Actions[0].Target != sentScreen {
		t.Errorf("некуда пойти разбираться: %v", к.Actions)
	}
}

// Показателю, которому идти некуда, кнопку не выдумываем.
func TestNoSpaceAlertHasNoButton(t *testing.T) {
	к := healthAlert(engine.HealthCard{Key: "выдуманный", Title: "Что-то", Value: "плохо", Level: "bad"})
	if len(к.Actions) != 0 {
		t.Errorf("выдумана кнопка в никуда: %v", к.Actions)
	}
}

// «1 стоят внимания» и «1 уведомлений» стояли на «Почте» живьём.
func TestNumbersInCaptionAreInflected(t *testing.T) {
	for n, надо := range map[int]string{1: "стоит", 2: "стоят", 5: "стоят", 11: "стоят", 21: "стоит"} {
		if got := plural2(n, "стоит", "стоят", "стоят"); got != надо {
			t.Errorf("%d: %q, а надо %q", n, got, надо)
		}
	}
}

package store

import (
	"strings"
	"sync"
	"testing"
)

// Счёт Server-Timing видит запросы своей горутины и только их: фоновая
// работа сердцебиения в счёт экрана не попадает, а после Stop счёт стоит.
func TestSQLTraceCountsOwnGoroutineOnly(t *testing.T) {
	s := testStore(t)
	tr := TraceSQL()
	for i := 0; i < 3; i++ {
		_ = s.Setting("ui.poll_s", "")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = s.Setting("ui.poll_s", "")
		}
	}()
	wg.Wait()
	if got := tr.Stats().N; got != 3 {
		t.Errorf("насчитано %d запросов, а своих было 3", got)
	}
	tr.Stop()
	_ = s.Setting("ui.poll_s", "")
	if got := tr.Stats().N; got != 3 {
		t.Errorf("после Stop счёт продолжился: %d", got)
	}
	if sqlTraced.Load() != 0 {
		t.Errorf("после Stop считающих горутин %d", sqlTraced.Load())
	}
}

// Вложенный замер отдаёт счёт внешнему, а после обоих Stop счёт выключен
// у всех: иначе каждый запрос каждой горутины платил бы за номер горутины.
func TestSQLTraceNested(t *testing.T) {
	s := testStore(t)
	outer := TraceSQL()
	_ = s.Setting("ui.poll_s", "")
	inner := TraceSQL()
	_ = s.Setting("ui.poll_s", "")
	_ = s.Setting("ui.poll_s", "")
	if got := inner.Stats().N; got != 2 {
		t.Errorf("вложенный насчитал %d, ждали 2", got)
	}
	inner.Stop()
	_ = s.Setting("ui.poll_s", "")
	outer.Stop()
	if got := outer.Stats().N; got != 4 {
		t.Errorf("внешний насчитал %d, ждали 4 (1 + 2 вложенных + 1)", got)
	}
	if n := sqlTraced.Load(); n != 0 {
		t.Errorf("после двух Stop считающих горутин %d", n)
	}
}

// Снимок настроек по приставке живёт до первой записи настроек: отметка
// прочтения, поставленная только что, видна на следующем же вызове.
func TestSettingsByPrefixSharedSeesWrites(t *testing.T) {
	s := testStore(t)
	_ = s.SetSetting("chat.read.1", "a")
	if got := s.SettingsByPrefixShared("chat.read.")["chat.read.1"]; got != "a" {
		t.Fatalf("снимок: %q", got)
	}
	tr := TraceSQL()
	_ = s.SettingsByPrefixShared("chat.read.")
	if n := tr.Stats().N; n != 0 {
		t.Errorf("повторный снимок без записи сходил в базу %d раз", n)
	}
	tr.Stop()
	_ = s.SetSetting("chat.read.2", "b")
	if got := s.SettingsByPrefixShared("chat.read.")["chat.read.2"]; got != "b" {
		t.Errorf("запись не видна в снимке: %q", got)
	}
}

// Счёт помнит самый долгий запрос (perf-pass 9.4): шаблон без значений и
// кто его позвал — метод хранилища и вызов снаружи. Вложенный замер отдаёт
// свой самый долгий внешнему.
func TestSQLTraceRemembersSlowest(t *testing.T) {
	s := testStore(t)
	outer := TraceSQL()
	_ = s.Setting("секретное.значение.в.аргументе", "")
	inner := TraceSQL()
	_, _ = s.CountMessages()
	inner.Stop()
	outer.Stop()
	slow := outer.Slowest()
	if slow.D <= 0 || slow.Query == "" {
		t.Fatalf("самый долгий запрос не запомнен: %+v", slow)
	}
	if strings.Contains(slow.Query, "секретное") {
		t.Errorf("в шаблон попало значение аргумента: %q", slow.Query)
	}
	// из теста в пакете хранилища вход в пакет — сам тест; снаружи — testing
	if !strings.HasPrefix(slow.Caller, "store.") || !strings.Contains(slow.Caller, " ← testing.") {
		t.Errorf("вызывающий не назван: %q", slow.Caller)
	}
	if in := inner.Slowest(); in.D > slow.D {
		t.Errorf("вложенный самый долгий (%v) не дошёл до внешнего (%v)", in.D, slow.D)
	}
}

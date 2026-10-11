package api

// Несколько учёток Telegram на экране: список учёток, закреплённая полоса
// с выбором и собеседники только выбранной. Одна учётка — полосы нет:
// подписывать нечего.

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const secondTG = "+70000000002"

var secondDigits = engine.NumberDigits(secondTG)

// telegramWithTwoAccounts — личная и рабочая, у каждой свой собеседник, и
// у обоих один и тот же номер чата: так проверяется, что пара «учётка +
// номер» и есть идентичность.
func telegramWithTwoAccounts(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := telegramWithChannels(t, 1)
	if err := st.SaveTGAccount(&store.TGAccount{Phone: secondTG, Title: "рабочая",
		State: "ok", Enabled: true, Every: 15, Batch: 100}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGChat(&store.TGChat{Account: secondTG, ID: 1, Title: "Отдел кадров",
		Kind: "group", Class: store.ClassSensitive}); err != nil {
		t.Fatal(err)
	}
	return s, st
}

func TestTelegramScreenTwoAccounts(t *testing.T) {
	s, _ := telegramWithTwoAccounts(t)

	// «Учётки»: обе строками, с номером и состоянием
	doc, err := s.buildScreen(tgScreen+"~"+tgTabConn, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	for _, надо := range []string{"личная", "рабочая", telegramAccount, secondTG, "Добавить аккаунт"} {
		if !strings.Contains(dump, надо) {
			t.Errorf("на «Учётках» нет %q", надо)
		}
	}
	if findByID(doc.Components, "tg-acc-"+secondDigits) == nil {
		t.Error("строки второй учётки нет")
	}

	// «Собеседники» первой учётки: полоса выбора и только её чаты
	doc, err = s.buildScreen(tgScreen+"@"+tgDigits+"~"+tgTabChats, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	полоса := findByID(doc.Components, "учётка-закреплена")
	if полоса == nil {
		t.Fatal("закреплённой полосы учётки нет: непонятно, чьи это собеседники")
	}
	if полоса.Subtitle != "личная" {
		t.Errorf("полоса подписана не той учёткой: %q", полоса.Subtitle)
	}
	if len(полоса.Selects) != 1 || len(полоса.Selects[0].Options) != 2 {
		t.Fatalf("в полосе нет выбора другой учётки: %+v", полоса.Selects)
	}
	dump = dumpScreen(doc)
	if !strings.Contains(dump, "Митя") || strings.Contains(dump, "Отдел кадров") {
		t.Errorf("в собеседниках личной учётки чужие чаты:\n%s", dump)
	}

	// вторая учётка: её собеседник, и чужого нет
	doc, err = s.buildScreen(tgScreen+"@"+secondDigits+"~"+tgTabChats, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump = dumpScreen(doc)
	if !strings.Contains(dump, "Отдел кадров") || strings.Contains(dump, "Митя") {
		t.Errorf("в собеседниках рабочей учётки чужие чаты:\n%s", dump)
	}
	// и права в клетках адресованы чату этой учётки, а не соседней: тот же номер
	// чата есть в обеих
	клетка := tgCell(t, tgMatrixOf(t, doc), "tg-1", "Читать")
	if !strings.HasPrefix(клетка.Action.Target, secondDigits+".") {
		t.Errorf("клетка рабочей учётки адресована не ей: %q", клетка.Action.Target)
	}

	// пределы обращений — свои у каждой учётки: поля «Тонкой настройки» с её
	// значениями, а сохранение адресовано ей
	форма := rareFormOf(rareBody(t, s, tgScreen+"@"+secondDigits+"~"+tgTabAnswer), "every")
	if форма == nil || форма.Submit == nil || форма.Submit.Action != "save_tg_rate" || форма.Submit.Target != secondDigits {
		t.Fatalf("пределы второй учётки не её: %+v", форма)
	}
	for _, f := range форма.Fields {
		want := map[string]string{"every": "15", "batch": "100"}[f.Name]
		if f.Value != want {
			t.Errorf("поле %q второй учётки показывает %q, а у неё %q", f.Name, f.Value, want)
		}
	}
}

// Переключение учётки списком в полосе ведёт на ту же вкладку, но к
// другой учётке.
func TestSwitchToAnotherTelegramAccount(t *testing.T) {
	s, _ := telegramWithTwoAccounts(t)
	куда, err := s.execute(actionRequest{Action: "goto_tg_account", Target: tgTabChats,
		Params: map[string]string{"value": "рабочая"}})
	if err != nil {
		t.Fatal(err)
	}
	if куда != tgScreen+"@"+secondDigits {
		t.Fatalf("переход ведёт не туда: %q", куда)
	}
	куда, err = s.execute(actionRequest{Action: "goto_tg_account", Target: tgTabAnswer,
		Params: map[string]string{"value": "личная"}})
	if err != nil {
		t.Fatal(err)
	}
	if куда != tgScreen+"@"+tgDigits+"~"+tgTabAnswer {
		t.Fatalf("переход потерял вкладку: %q", куда)
	}
	if _, err := s.execute(actionRequest{Action: "goto_tg_account",
		Params: map[string]string{"value": "выдуманная"}}); err == nil {
		t.Error("переход к несуществующей учётке прошёл")
	}
}

// Право выдаётся чату конкретной учётки: тот же номер в соседней остаётся
// как был.
func TestChatGrantDoesNotLeakBetweenAccounts(t *testing.T) {
	s, st := telegramWithTwoAccounts(t)
	if _, err := s.execute(actionRequest{Action: "tg_chat_right",
		Target: tgTarget(secondTG, 1) + "~collect"}); err != nil {
		t.Fatal(err)
	}
	рабочий, _ := st.TGChat(secondTG, 1)
	личный, _ := st.TGChat(telegramAccount, 1)
	if !рабочий.Collect {
		t.Error("право не выдалось чату рабочей учётки")
	}
	if личный.Collect {
		t.Error("право протекло в личную учётку")
	}
	// и одна учётка — полосы нет: экран как прежде
	if err := s.eng.TGForget(secondTG); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen(tgScreen+"~"+tgTabChats, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if findByID(doc.Components, "учётка-закреплена") != nil {
		t.Error("у одной учётки появилась полоса выбора")
	}
}

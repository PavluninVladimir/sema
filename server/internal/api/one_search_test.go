package api

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// На экране одно поле поиска. У «Telegram» их было два: общий поиск по
// настройкам сверху и поиск внутри списка собеседников — «зачем два
// поиска» (13 сентября 2026). Верхнее остаётся, части своего не рисуют,
// а набранное сверху уходит и в них (app.js, загрузитьЧасть).
func TestOneSearchPerScreen(t *testing.T) {
	s, _ := telegramWithChannels(t, 3)
	doc, err := s.buildScreen("settings:telegram", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Searchable {
		t.Fatal("верхнего поля поиска нет")
	}
	var полей int
	var walk func(б []web.Block)
	walk = func(б []web.Block) {
		for _, x := range б {
			if x.FragmentSearch != "" {
				полей++
			}
			walk(x.Children)
		}
	}
	walk(toScreen(doc).Blocks)
	if полей != 0 {
		t.Errorf("кроме верхнего поля поиска на экране ещё %d — должно быть одно", полей)
	}
}

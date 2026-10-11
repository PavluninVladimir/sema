package engine

import (
	"context"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Принять цену» меняет цену и закрывает карточку: открытая карточка после
// принятой цены выглядит как нерабочая кнопка.
func TestAcceptPriceClosesCard(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "облако", Type: "openai", BaseURL: "https://api.example.com/v1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	// открытая карточка старого вида — до появления общей карточки цен
	// провайдера; кнопка обязана работать, пока такие лежат в «Уведомлениях»
	if _, err := e.Notify(&store.Notification{
		Key: "price:облако", Title: "Цена модели изменилась: облако",
		Actions: []store.NotifyAction{
			{Kind: "core", Label: "Принять цену", Op: "accept_price", Ref: "облако|$1 / $2 / 1M"},
			{Kind: "dismiss", Label: "Не менять"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := st.Notifications(true, 10)
	var id int64
	for _, n := range list {
		if n.Key == "price:облако" {
			id = n.ID
		}
	}
	if id == 0 {
		t.Fatal("карточки цены нет")
	}
	if err := e.NotifyDecide(context.Background(), id, 0, "Вадим"); err != nil {
		t.Fatal(err)
	}
	if e.PriceOf("облако") != "$1 / $2 / 1M" {
		t.Fatalf("цена не применилась: %q", e.PriceOf("облако"))
	}
	n, ok := st.Notification(id)
	if !ok || n.State != store.NotifyDone {
		t.Fatalf("карточка не закрылась: %+v", n)
	}
}

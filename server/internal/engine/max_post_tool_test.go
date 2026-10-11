package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Публикация в MAX держит границы сама: только каналы, только с правом
// «Писать», не больше предела в сутки. До сети дело не доходит.
func TestPostInMAXHoldsBounds(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	if err := st.SaveMAXChat(&store.MAXChat{ID: 1, Title: "Технический форум", Kind: "channel"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMAXChat(&store.MAXChat{ID: 2, Title: "Семья", Kind: "chat", Send: true}); err != nil {
		t.Fatal(err)
	}
	post := func(channel string) error {
		_, err := e.toolMAXPost(context.Background(), []any{channel, "Подборка хайпа"})
		return err
	}
	if err := post("Семья"); err == nil || !strings.Contains(err.Error(), "не канал") {
		t.Errorf("в группу дали публиковать: %v", err)
	}
	if err := post("Нет такого"); err == nil || !strings.Contains(err.Error(), "нет среди") {
		t.Errorf("несуществующий канал: %v", err)
	}
	if err := post("Технический форум"); err == nil || !strings.Contains(err.Error(), "Писать") {
		t.Errorf("без права «Писать» дали публиковать: %v", err)
	}
	if err := st.SetMAXChatRight(1, "send", true); err != nil { // право выдаёт человек переключателем
		t.Fatal(err)
	}
	_ = st.SetSetting("max.posts_per_day", "1")
	_ = st.SetSetting(maxCountKey(&store.MAXChat{ID: 1}, time.Now()), "1")
	if err := post("технический форум"); err == nil || !strings.Contains(err.Error(), "предел") {
		t.Errorf("предел в сутки не держится: %v", err)
	}
	if _, ok := primitives["max"]; !ok {
		t.Error("права max нет среди прав модулей")
	}
}

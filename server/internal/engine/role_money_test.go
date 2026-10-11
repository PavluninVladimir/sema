package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Кончились деньги у провайдера — так и сказано, а не «ключ не подошёл».
func TestMoneyOfProviderRanOut(t *testing.T) {
	err := errors.New(`gpt-5.6-luna: HTTP 401: {"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":401}}`)
	if !moneyRanOut(err) {
		t.Fatal("закончившиеся деньги не узнаны")
	}
	if h := humanModelErr(err).Error(); !strings.Contains(h, "закончились деньги") {
		t.Fatalf("человеку сказано не то: %q", h)
	}
	if moneyRanOut(errors.New("HTTP 401: invalid_api_key")) {
		t.Error("неверный ключ принят за пустой счёт")
	}
	e, st := newTestEngine(t, "модель", nil)
	for i := 0; i < roleFailsBeforeRest; i++ {
		e.countRoleFail("Память", "gpt-5.6-luna", err)
	}
	list, _ := st.Notifications(true, 20)
	for _, n := range list {
		if n.Key == "role:Память" {
			if !strings.Contains(n.Title, "закончились деньги") {
				t.Fatalf("заголовок не про деньги: %q", n.Title)
			}
			return
		}
	}
	t.Fatal("уведомления о роли нет")
}

// Успешный ответ в чате снимает «Роль «Чаты»: модель не отвечает» — как
// успешный вызов роли в фоне.
func TestSuccessInChatClearsNotificationRole(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "отвечаю"})
	for i := 0; i < roleFailsBeforeRest; i++ {
		e.countRoleFail(RoleChat, "модель", errors.New("context deadline exceeded"))
	}
	open := func() bool {
		list, _ := st.Notifications(true, 20)
		for _, n := range list {
			if n.Key == "role:"+RoleChat && n.State == store.NotifyOpen {
				return true
			}
		}
		return false
	}
	if !open() {
		t.Fatal("уведомление о роли не завелось")
	}
	chatID, _ := st.CreateChat("проба")
	if err := e.ChatReply(context.Background(), chatID, "привет"); err != nil {
		t.Fatal(err)
	}
	if open() {
		t.Fatal("модель ответила в чате, а уведомление «не отвечает» осталось")
	}
}

// Успешная проверка модели снимает передышку ролей, где она основная.
func TestCheckModelClearsCooldown(t *testing.T) {
	e, st := newTestEngine(t, "модель", &fakeProvider{answer: "готов"})
	r := e.Role(RoleChat)
	for i := 0; i < roleFailsBeforeRest; i++ {
		e.countRoleFail(RoleChat, r.Model, errors.New("HTTP 401 insufficient_quota"))
	}
	if st.Setting("role.cooldown."+RoleChat, "") == "" {
		t.Fatal("передышка не началась")
	}
	if _, err := e.CheckModel(context.Background(), r.Model); err != nil {
		t.Fatal(err)
	}
	if st.Setting("role.cooldown."+RoleChat, "") != "" {
		t.Fatal("модель ответила на проверку, а роль осталась на передышке")
	}
}

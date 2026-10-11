package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// У замечания «модель не тянет роль» есть кнопка «Закрыть»: без неё оно
// висело неделями с подписью «модель сменили», и человек спрашивал «как
// закрыть» (13 сентября 2026). Главная кнопка — по смыслу: устарело —
// закрыть, живое — к ролям.
func TestRoleFindingCanBeDismissed(t *testing.T) {
	s, _ := testServer(t)
	roles := s.eng.Roles()
	for i := range roles {
		if roles[i].Name == engine.RoleChat {
			roles[i].Model = "НоваяМодель"
			roles[i].LastErr = "СтараяМодель · 09.09 22:37 · нет связи с сетью"
		}
	}
	if err := s.eng.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	замечания := s.modelFindings(nil, s.eng.Roles())
	var строка *Component
	for i := range замечания {
		if len(замечания[i].Badges) > 0 && замечания[i].Badges[0].Text == "модель сменили" {
			строка = &замечания[i]
		}
	}
	if строка == nil {
		t.Fatal("замечания с подписью «модель сменили» нет")
	}
	if len(строка.Actions) < 2 || строка.Actions[0].Action != "dismiss_role_err" || строка.Actions[0].Style != "primary" {
		t.Fatalf("у устаревшего замечания главное действие не «Закрыть»: %+v", строка.Actions)
	}
	if _, err := s.execute(actionRequest{Action: "dismiss_role_err", Target: engine.RoleChat}); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.eng.Roles() {
		if r.Name == engine.RoleChat && r.LastErr != "" {
			t.Errorf("отметка не снята: %q", r.LastErr)
		}
	}
	// живое замечание: главное — к ролям, «Закрыть» в запасе
	roles = s.eng.Roles()
	for i := range roles {
		if roles[i].Name == engine.RoleChat {
			roles[i].LastErr = "НоваяМодель · 13.09 11:00 · нет связи с сетью"
		}
	}
	_ = s.eng.SaveRoles(roles)
	for _, з := range s.modelFindings(nil, s.eng.Roles()) {
		if len(з.Actions) >= 2 && з.Actions[1].Action == "dismiss_role_err" {
			if з.Actions[0].Action != "$goto" || з.Actions[0].Style != "primary" {
				t.Errorf("у живого замечания главное действие %+v", з.Actions[0])
			}
			return
		}
	}
	t.Error("живое замечание без кнопки «Закрыть»")
}

// Роль «Эмбеддинги» смотрит на служебную модель эмбеддера, и в списке
// подключённых её не бывает: замечание «модели нет в списке» про неё —
// ложная тревога (13 сентября 2026).
func TestEmbedderIsNotFalseAlert(t *testing.T) {
	s, _ := testServer(t)
	roles := s.eng.Roles()
	for i := range roles {
		if roles[i].Name == engine.RoleEmbed {
			roles[i].Model = "text-embedding-bge-m3" // как на живом: служебная модель эмбеддера
		}
	}
	// подключённые модели есть, эмбеддера среди них нет — как на живом
	models := []engine.Model{{Name: "deepseek-flash"}}
	for _, з := range s.modelFindings(models, roles) {
		if strings.Contains(з.Subtitle, engine.RoleEmbed) {
			t.Errorf("замечание про эмбеддер вернулось: %q", з.Subtitle)
		}
	}
}

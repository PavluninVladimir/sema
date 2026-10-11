package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Отметка о сбое — запись о прошлом, а роль могли переназначить. 10
// сентября 2026 «Стоит посмотреть» объявило «deepseek-flash не тянет роль
// «Чаты»» про сбой, которого у этой модели не было вовсе: заголовок брал
// модель из роли, а тело — из отметки.
func TestTitleNamesFailedModel(t *testing.T) {
	подвела, время, причина := parseMark("DeepseekPro · 09.09 22:37 · нет связи с сетью")
	if подвела != "DeepseekPro" {
		t.Errorf("модель из отметки: %q", подвела)
	}
	if время != "09.09 22:37" {
		t.Errorf("время из отметки: %q", время)
	}
	if причина != "нет связи с сетью" {
		t.Errorf("причина: %q", причина)
	}
	// причина сама содержит разделители — она не должна обрезаться
	_, _, длинная := parseMark("qwen3 · 10.09 14:47 · секретное не отдаётся · назначьте доверенную")
	if !strings.Contains(длинная, "назначьте доверенную") {
		t.Errorf("причина обрезана: %q", длинная)
	}
	// старая отметка без нашего формата не даёт выдуманной модели
	м, _, целиком := parseMark("просто текст без разделителей")
	if м != "" || целиком != "просто текст без разделителей" {
		t.Errorf("из чужого вида выужена модель %q", м)
	}
	// и строка, где первая часть — не модель, а часть причины
	м2, _, _ := parseMark("что-то · пошло · не так")
	if м2 != "" {
		t.Errorf("«что-то» принято за модель: %q", м2)
	}
}

// Текст не должен противоречить сам себе: «запасной у неё нет — работа
// просто не делается. Молчаливый переход на запасную…» человек читал как
// насмешку.
func TestFindingDoesNotContradictItself(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.AddModel(engine.ModelConf{Name: "быстрая", Type: "openai",
		BaseURL: "https://шлюз/v1", Model: "быстрая"}); err != nil {
		t.Fatal(err)
	}
	пометить := func(role, отметка, запасная string) *Component {
		t.Helper()
		roles := s.eng.Roles()
		for i := range roles {
			if roles[i].Name == role {
				roles[i].LastErr = отметка
				roles[i].Spare = запасная
				roles[i].Model = "быстрая"
			}
		}
		if err := s.eng.SaveRoles(roles); err != nil {
			t.Fatal(err)
		}
		замечания := s.modelFindings(nil, s.eng.Roles())
		if len(замечания) == 0 {
			t.Fatal("замечаний нет")
		}
		for i := range замечания {
			if strings.Contains(замечания[i].Subtitle, role) {
				return &замечания[i]
			}
		}
		t.Fatalf("замечания про роль %q нет", role)
		return nil
	}

	// без запасной: про переход на запасную ни слова
	без := пометить(engine.RoleModules, "DeepseekFlash · 10.09 14:47 · секретное не отдаётся", "нет")
	if strings.Contains(без.Caption, "Молчаливый переход") {
		t.Errorf("у роли без запасной обещан переход на запасную: %q", без.Caption)
	}
	if !strings.Contains(без.Caption, "работа просто не делается") {
		t.Errorf("не сказано, что работа не делается: %q", без.Caption)
	}
	// заголовок называет ту модель, что подвела, а не ту, что стоит сейчас
	if !strings.HasPrefix(без.Subtitle, "DeepseekFlash") {
		t.Errorf("заголовок: %q", без.Subtitle)
	}
	if !strings.Contains(без.Caption, "с тех пор на роли стоит быстрая") {
		t.Errorf("не сказано, что модель сменили: %q", без.Caption)
	}

	// с запасной: сказано и про неё, и про двойную плату
	с := пометить(engine.RoleChat, "быстрая · 10.09 15:00 · нет связи с сетью", "запасная-2")
	if !strings.Contains(с.Caption, "запасная запасная-2") {
		t.Errorf("запасная не названа: %q", с.Caption)
	}
	if !strings.Contains(с.Caption, "платите вы за обе") {
		t.Errorf("не сказано про двойную плату: %q", с.Caption)
	}
	// модель та же — про смену не пишем
	if strings.Contains(с.Caption, "с тех пор на роли стоит") {
		t.Errorf("выдумана смена модели: %q", с.Caption)
	}
}

// Роль может смотреть на модель, которой нет вовсе: старые модели DeepSeek
// удалили, а запасная у роли «Чаты» осталась на «DeepseekFlash».
// Обращение к ней кончится «модель не подключена» ровно тогда, когда
// основная подведёт, — то есть в самый неподходящий момент.
func TestRoleOnRemoteModelVisible(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.AddModel(engine.ModelConf{Name: "живая", Type: "openai",
		BaseURL: "https://шлюз/v1", Model: "живая"}); err != nil {
		t.Fatal(err)
	}
	roles := s.eng.Roles()
	for i := range roles {
		if roles[i].Name == engine.RoleChat {
			roles[i].Model, roles[i].Spare, roles[i].LastErr = "живая", "удалённая", ""
		}
	}
	if err := s.eng.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	// замечания живут на своей внутренней вкладке «Проблемы»
	doc, err := s.buildScreen("settings:models~issues", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	if !strings.Contains(dump, "запасная удалённая") {
		t.Error("Сёма не заметил, что запасная модель роли удалена")
	}
	if !strings.Contains(dump, "модели нет в списке") {
		t.Errorf("замечания нет:\n%s", clipTest(dump))
	}
	// на «Ролях» замечания не видно — значит о нём должен говорить
	// счётчик у имени вкладки, иначе беда просто спрятана
	роли, err := s.buildScreen("settings:models", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	счёт := ""
	for _, в := range роли.Subtabs {
		if strings.HasSuffix(в.ID, "~issues") {
			счёт = в.Badge
		}
	}
	if счёт == "" || счёт == "0" {
		t.Errorf("вкладка «Проблемы» без счётчика: %q — беда спрятана", счёт)
	}
	// а про живую основную молчим
	if strings.Contains(dump, "основная живая") {
		t.Error("живая модель объявлена удалённой")
	}
}

package api

import (
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Образец окна настроек, который подсказка и скилл «Модуль-интеграция» дают
// модели, работает по-настоящему: экран модуля собирается, «Сохранить» зовёт
// функцию модуля, и экран показывает сохранённое. 4 октября 2026 модуль
// Дневник вместо окна завёл функцию settingsSchema(), которую никто не
// показывает, — образца у модели не было.
func TestModuleSettingsScreenSampleWorks(t *testing.T) {
	s, _ := testServer(t)
	raw := `{"name": "dnevnik", "title": "Дневник", "needs": ["ui"],
 "screens": [{"id": "settings", "title": "Настройки", "build": "settingsScreen"}]}
---
function settingsScreen() {
  const s = sema.data.get('settings', 'main') || {portal: 'https://sgo.example.ru', children: '', every: '6'};
  return {heading: 'Дневник', components: [{type: 'form', title: 'Настройки',
    fields: [
      {type: 'text', name: 'portal', label: 'Адрес портала', value: s.portal},
      {type: 'text', name: 'children', label: 'Дети через запятую', value: s.children},
      {type: 'text', name: 'every', label: 'Проверять раз в столько часов', value: s.every}],
    submit: {action: 'module_action', target: 'dnevnik~saveSettings', label: 'Сохранить'}}]};
}
function saveSettings(p) {
  sema.data.put('settings', 'main', {portal: p.portal, children: p.children, every: p.every});
}`
	m, err := engine.ParseModule(raw)
	if err != nil {
		t.Fatalf("образец не разобрался: %v", err)
	}
	if err := s.eng.InstallModule(m); err != nil {
		t.Fatalf("образец не встал: %v", err)
	}
	if err := s.eng.EnableModule("dnevnik", true); err != nil {
		t.Fatal(err)
	}
	form := func() Component {
		t.Helper()
		doc, _ := buildAll(t, s, "module:dnevnik:settings")
		for _, c := range doc.Components {
			if c.Type == "form" {
				return c
			}
		}
		t.Fatalf("на экране модуля нет формы: %+v", doc.Components)
		return Component{}
	}
	f := form()
	if f.Submit == nil || f.Submit.Action != "module_action" || f.Submit.Target != "dnevnik~saveSettings" || len(f.Fields) != 3 {
		t.Fatalf("форма настроек: %+v", f)
	}
	if f.Fields[0].Value != "https://sgo.example.ru" {
		t.Errorf("умолчание адреса: %q", f.Fields[0].Value)
	}
	if _, err := s.execute(actionRequest{Action: f.Submit.Action, Target: f.Submit.Target,
		Params: map[string]string{"portal": "https://sgo.example.ru", "children": "Анна, Мила", "every": "3"}}); err != nil {
		t.Fatalf("«Сохранить»: %v", err)
	}
	f = form()
	if f.Fields[0].Value != "https://sgo.example.ru" || f.Fields[1].Value != "Анна, Мила" || f.Fields[2].Value != "3" {
		t.Fatalf("экран не показывает сохранённое: %+v", f.Fields)
	}
}

// У ждущего поручения есть и «Продолжить», и «Остановить»: заранее
// заведённые заглушки секретов иначе вернули бы его в работу сами.
func TestWaitingAssignmentCanBePaused(t *testing.T) {
	s, _ := testServer(t)
	_, a := assignFixture(t, s)
	a.State = store.AssignWaiting
	if err := s.eng.Store().SaveAssignment(a); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, act := range assignActions(a) {
		got[act.Action] = true
	}
	if !got["assign_pause"] || !got["assign_resume"] || !got["assign_cancel"] {
		t.Fatalf("действия ждущего поручения: %v", got)
	}
	if _, err := s.execute(actionRequest{Action: "assign_pause", Target: strconv.FormatInt(a.ID, 10)}); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.eng.Store().Assignment(a.ID); b.State != store.AssignPaused {
		t.Fatalf("после «Остановить»: %s", b.State)
	}
}

// Экран добавления секрета, куда ведёт карточка «Нужен секрет», открывается
// с видом «Пароль»: с «Ключ SSH — сгенерировать» поля значения не видно.
func TestSecretAddByNameDefaultsToPassword(t *testing.T) {
	s, _ := testServer(t)
	kindOf := func(screen string) string {
		doc, _ := buildAll(t, s, screen)
		var kind string
		walkAll(doc.Components, func(c Component) {
			for _, f := range c.Fields {
				if f.Name == "kind" {
					kind = f.Value
				}
			}
		})
		return kind
	}
	if got := kindOf(engine.SecretAddScreen("netscool:login")); got != secretKindPassword {
		t.Errorf("с именем: вид %q", got)
	}
	if got := kindOf(engine.SecretScreen("new", "")); !strings.HasPrefix(got, "Ключ SSH") {
		t.Errorf("без имени вид сменился: %q", got)
	}
}

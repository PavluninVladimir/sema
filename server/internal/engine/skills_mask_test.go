package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Маска из памяти, начинающаяся с буквы, — приставка токена и сравнивается
// с начала слова: «sk-» внутри «--disk-usage» не секрет, а «sk-abc…» после
// пробела — секрет. Маска со знака ищется как есть.
func TestSecretMaskMatchesFromWordStart(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	for _, m := range []string{"sk-", "enc:v1:", "Bearer "} {
		if err := st.AddMemoryRule("mask", m, "тест"); err != nil {
			t.Fatalf("маска %q: %v", m, err)
		}
	}
	cases := []struct {
		text string
		want string
	}{
		{"`journalctl --disk-usage` — рост журналов", ""},
		{"flask-restful и task-runner", ""},
		{"ключ sk-abc123def456 в тексте", "sk-"},
		{"sk-abc123 в начале строки", "sk-"},
		{"token=\"sk-abc123\"", "sk-"},
		{"значение enc:v1:deadbeef", "enc:v1:"},
		{"Authorization: Bearer xyz", "Bearer "},
	}
	for _, c := range cases {
		if got := e.secretMask(c.text); got != c.want {
			t.Errorf("%q: маска %q, ждали %q", c.text, got, c.want)
		}
	}
}

// Правка скилла проверяется по новым строкам: строка, которая уже лежала в
// базе, правку не роняет — иначе встроенный «Linux-сервер» с
// `--disk-usage` не правился вовсе. Новая строка с секретом — отказ.
func TestSaveSkillChecksOnlyNewLines(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.AddMemoryRule("mask", "secret-", "тест"); err != nil {
		t.Fatalf("маска: %v", err)
	}
	old := &store.Skill{Name: "mask-probe", Title: "Проба", Source: store.SkillBuiltin, Enabled: true,
		Text: "первая строка\nсюда попало secret-xyz из сборки\nтретья"}
	if err := st.SaveSkill(old); err != nil {
		t.Fatalf("исходный скилл: %v", err)
	}
	edited := *old
	edited.Text = old.Text + "\nновая безобидная строка"
	if err := e.SaveSkill(&edited, "тест"); err != nil {
		t.Fatalf("правка без секрета в новых строках отвергнута: %v", err)
	}
	bad := *old
	bad.Text = old.Text + "\nмой ключ secret-123"
	err := e.SaveSkill(&bad, "тест")
	if err == nil || !strings.Contains(err.Error(), "строка 4") {
		t.Fatalf("новая строка с секретом должна быть отвергнута с номером строки, ошибка: %v", err)
	}
	fresh := &store.Skill{Name: "new-skill", Title: "Новый", Source: store.SkillOwn, Enabled: true,
		Text: "ключ secret-123"}
	if err := e.SaveSkill(fresh, "тест"); err == nil {
		t.Fatal("новый скилл с секретом сохранён")
	}
}

// Встроенный «Linux-сервер» правится: его собственный текст не считается
// секретом ни масками первичного набора, ни регулярным выражением.
func TestBuiltinLinuxSkillIsEditable(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	e.ensureMemorySeed()
	e.loadBuiltinSkills()
	k, ok := e.store.Skill("linux-admin")
	if !ok {
		t.Skip("встроенного linux-admin нет")
	}
	if !strings.Contains(k.Text, "--disk-usage") {
		t.Skip("в скилле нет --disk-usage, проверка не про него")
	}
	k.Text += "\nДописано человеком."
	if err := e.SaveSkill(k, "тест"); err != nil {
		t.Fatalf("правка встроенного скилла отвергнута: %v", err)
	}
}

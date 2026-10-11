package engine

import (
	"strings"
	"testing"
)

// Новый пресет провайдера появляется в уже заполненном справочнике, а
// удалённый человеком не возвращается.
func TestProvidersTopUp(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	first := e.Providers()
	if len(first) == 0 {
		t.Fatal("справочник пуст")
	}
	// человек убрал один пресет
	var trimmed []ProviderConf
	for _, p := range first {
		if p.Name != "Groq" {
			trimmed = append(trimmed, p)
		}
	}
	if err := e.SaveProviders(trimmed); err != nil {
		t.Fatal(err)
	}
	got := e.Providers()
	for _, p := range got {
		if p.Name == "Groq" {
			t.Error("удалённый провайдер вернулся сам")
		}
	}
	// а новый пресет должен доехать: метку сбрасываем, как при обновлении
	if err := e.store.SetSetting(providersSeedKey, "1"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range e.Providers() {
		if p.Kind == "perplexity" {
			found = true
		}
	}
	if !found {
		t.Error("новый пресет не досыпался")
	}
}

// Переименование пресета применяется и после того, как метка досыпки уже
// стоит: иначе в списке остаются близнецы.
func TestProvidersRenameAfterSeed(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	list := e.Providers() // первое заполнение ставит метку
	old := append([]ProviderConf{}, list...)
	old = append(old,
		ProviderConf{Name: "Perplexity (поиск)", Kind: "perplexity"},
		ProviderConf{Name: "Perplexity · модели", Kind: "openai"})
	if err := e.SaveProviders(old); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range e.Providers() {
		if p.Name == "Perplexity" {
			n++
			if !p.Catalog || p.Kind != "perplexity" {
				t.Errorf("переименованный пресет остался старым: %+v", p)
			}
		}
		if strings.HasPrefix(p.Name, "Perplexity ") {
			t.Errorf("старое имя осталось: %q", p.Name)
		}
	}
	if n != 1 {
		t.Errorf("после переименования Perplexity встречается %d раз", n)
	}
}

// Устаревший пресет (памяти о засеянном ещё нет — старая установка)
// принимает нынешний вид, а правка человека переживает обновление.
func TestProvidersRefreshRespectsEdits(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	list := e.Providers()
	var stale []ProviderConf
	for _, p := range list {
		if p.Name == "Perplexity" {
			p.Catalog, p.Hint = false, "старая подсказка"
		}
		stale = append(stale, p)
	}
	if err := e.SaveProviders(stale); err != nil {
		t.Fatal(err)
	}
	// старая установка: справочник есть, памяти о засеянном нет
	if err := st.SetSetting(providersMineKey, ""); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range e.Providers() {
		if p.Name == "Perplexity" {
			found = true
			if !p.Catalog || p.Hint == "старая подсказка" {
				t.Errorf("устаревший пресет не обновился: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("пресета нет вовсе")
	}

	// теперь память есть — правка человека остаётся
	var edited []ProviderConf
	for _, p := range e.Providers() {
		if p.Name == "DeepSeek" {
			p.BaseURL = "https://свой-шлюз/v1"
		}
		edited = append(edited, p)
	}
	if err := e.SaveProviders(edited); err != nil {
		t.Fatal(err)
	}
	for _, p := range e.Providers() {
		if p.Name == "DeepSeek" && p.BaseURL != "https://свой-шлюз/v1" {
			t.Errorf("правка человека затёрта: %q", p.BaseURL)
		}
	}
}

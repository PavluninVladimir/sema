package engine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Провайдер переименовал модели — старые остаются в списке и молчат.
// 10 сентября 2026 DeepSeek отдал «deepseek-v4-pro» и «deepseek-flash»
// вместо «deepseek-reasoner» и «deepseek-chat»: новые подключились, старые
// остались, роли смотрели на них, а экран не говорил ни слова.
func TestMissingOfProviderVisible(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	provider := "DeepSeek"
	if err := e.AddProvider(ProviderConf{Name: provider, Kind: "openai",
		BaseURL: "https://api.deepseek.com/v1", Catalog: true, NeedKey: true}); err != nil {
		t.Fatal(err)
	}
	add := func(name, model string) {
		if err := e.AddModel(ModelConf{Name: name, Type: "openai",
			BaseURL: "https://api.deepseek.com/v1", Model: model}); err != nil {
			t.Fatal(err)
		}
	}
	add("DeepseekPro", "deepseek-reasoner")
	add("DeepseekFlash", "deepseek-chat")
	add("deepseek-v4-pro", "deepseek-v4-pro")

	// пока не спрашивали — никто не пропал: «не знаем» и «нет у
	// провайдера» разные вещи, и путать их значит оболгать живые модели
	if p := e.GoneAtProvider(provider); len(p) != 0 {
		t.Errorf("до опроса объявлены пропавшими: %v", p)
	}
	if e.ModelGone("DeepseekPro") {
		t.Error("модель объявлена пропавшей до опроса провайдера")
	}

	// провайдер ответил новым списком
	if err := e.RememberCatalog(provider, []CatalogModel{
		{ID: "deepseek-v4-pro"}, {ID: "deepseek-flash"},
	}); err != nil {
		t.Fatal(err)
	}
	gone := e.GoneAtProvider(provider)
	if len(gone) != 2 {
		t.Fatalf("пропавших %d: %v", len(gone), gone)
	}
	if !e.ModelGone("DeepseekFlash") || e.ModelGone("deepseek-v4-pro") {
		t.Error("пропавшие и живые перепутаны")
	}

	// и роли, которые на них смотрят, названы поимённо: молча пропавшая
	// модель у роли — это отказ при первом же обращении
	if err := e.SetRoleField(RoleFilter, "model", "DeepseekPro"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetRoleField(RoleChat, "model", "deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	role := e.RolesOnMissing(gone)
	if !containsRole(role, RoleFilter) {
		t.Errorf("роль на пропавшей модели не названа: %v", role)
	}
	if containsRole(role, RoleChat) {
		t.Errorf("роль на живой модели названа пропавшей: %v", role)
	}
}

// Пустой ответ провайдера не запоминается: один сбой объявил бы пропавшими
// все модели разом. Провайдера здесь играет поддельный сервер — в сеть
// тест не ходит.
func TestEmptyReplyNotRemembered(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	empty := true
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if empty {
			_, _ = w.Write([]byte(`{"data": []}`))
			return
		}
		_, _ = w.Write([]byte(`{"data": [{"id": "своя"}]}`))
	}))
	defer host.Close()

	provider := "Свой шлюз"
	if err := e.AddProvider(ProviderConf{Name: provider, Kind: "openai",
		BaseURL: host.URL + "/v1", Catalog: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddModel(ModelConf{Name: "своя", Type: "openai",
		BaseURL: host.URL + "/v1", Model: "своя"}); err != nil {
		t.Fatal(err)
	}

	// сбой: список пуст. Запоминать это нельзя
	result, err := e.RefreshProviderModels(t.Context(), provider, addsNothing)
	if err == nil {
		t.Errorf("пустой список принят как ответ: %q", result)
	} else if !strings.Contains(err.Error(), "пуст") && !strings.Contains(err.Error(), "ни одной модели") {
		t.Errorf("отказ непонятен: %v", err)
	}
	if p := e.GoneAtProvider(provider); len(p) != 0 {
		t.Errorf("после сбоя живая модель объявлена пропавшей: %v", p)
	}

	// а нормальный ответ запоминается, и итог говорит человеческими словами
	empty = false
	result, err = e.RefreshProviderModels(t.Context(), provider, addsNothing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "у провайдера") {
		t.Errorf("итог обновления: %q", result)
	}
	if p := e.GoneAtProvider(provider); len(p) != 0 {
		t.Errorf("живая модель объявлена пропавшей: %v", p)
	}
}

func addsNothing([]CatalogModel) (int, int, error) { return 0, 0, nil }

func containsRole(list []string, name string) bool {
	for _, r := range list {
		if r == name {
			return true
		}
	}
	return false
}

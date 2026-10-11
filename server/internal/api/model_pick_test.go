package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// В списке моделей полсотни имён, и по имени не понять, чьё оно:
// «gpt-5.6-luna» бывает и у OpenAI, и у чужого шлюза, а «grok-4.6» и
// «kimi-k3» не говорят ничего вовсе.
func TestModelPickNamesProvider(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.AddProvider(engine.ProviderConf{Name: "OpenAI", Kind: "openai",
		BaseURL: "https://api.openai.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.eng.AddProvider(engine.ProviderConf{Name: "Свой шлюз", Kind: "openai",
		BaseURL: "http://шлюз.дома/v1"}); err != nil {
		t.Fatal(err)
	}
	// одно и то же имя у двух провайдеров — ровно тот случай, ради
	// которого всё и делается
	if err := s.eng.AddModel(engine.ModelConf{Name: "gpt-5.6-luna", Type: "openai",
		BaseURL: "https://api.openai.com/v1", Model: "gpt-5.6-luna"}); err != nil {
		t.Fatal(err)
	}
	if err := s.eng.AddModel(engine.ModelConf{Name: "своя-luna", Type: "openai",
		BaseURL: "http://шлюз.дома/v1", Model: "gpt-5.6-luna"}); err != nil {
		t.Fatal(err)
	}

	список, label := s.modelsWithProvider()
	has := func(что string) bool {
		for _, о := range список {
			if о == что {
				return true
			}
		}
		return false
	}
	if !has("gpt-5.6-luna · OpenAI") {
		t.Errorf("в списке нет модели с провайдером: %v", список)
	}
	if !has("своя-luna · Свой шлюз") {
		t.Errorf("в списке нет второй модели с провайдером: %v", список)
	}
	// значение выбора подписано так же, иначе выпадашка покажет пустоту
	// вместо назначенной модели
	if label("gpt-5.6-luna") != "gpt-5.6-luna · OpenAI" {
		t.Errorf("подпись значения: %q", label("gpt-5.6-luna"))
	}
	// «нет» — это не модель, его не подписываем
	if label("нет") != "нет" {
		t.Errorf("«нет» подписано провайдером: %q", label("нет"))
	}

	// модель с чужого шлюза: провайдера в справочнике нет, и вместо имени
	// пришёл бы адрес — «qwen3 · http://llm:8080/v1» не отвечает на вопрос
	// «чья модель»
	if err := s.eng.AddModel(engine.ModelConf{Name: "qwen3", Type: "openai",
		BaseURL: "http://llm:8080/v1", Model: "qwen3"}); err != nil {
		t.Fatal(err)
	}
	_, подпись2 := s.modelsWithProvider()
	got := подпись2("qwen3")
	if strings.Contains(got, "://") {
		t.Errorf("в подписи адрес вместо имени: %q", got)
	}
	if got != "qwen3" && !strings.Contains(got, "llm:8080") && !strings.Contains(got, "на этой машине") {
		t.Errorf("подпись локальной модели: %q", got)
	}

	// а в роль ложится ИМЯ, а не строка экрана: роль указывает на модель
	if modelName("gpt-5.6-luna · OpenAI") != "gpt-5.6-luna" {
		t.Errorf("имя из строки экрана: %q", modelName("gpt-5.6-luna · OpenAI"))
	}
	if modelName("qwen3") != "qwen3" {
		t.Error("имя без провайдера испорчено")
	}
}

// Выбор в роли доходит до роли: человек выбирает строку с провайдером, а
// в настройках должно оказаться имя модели.
func TestRoleGetsNameWithoutProvider(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.AddProvider(engine.ProviderConf{Name: "OpenAI", Kind: "openai",
		BaseURL: "https://api.openai.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.eng.AddModel(engine.ModelConf{Name: "gpt-5.6-luna", Type: "openai",
		BaseURL: "https://api.openai.com/v1", Model: "gpt-5.6-luna"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "set_role_model", Target: engine.RoleChat,
		Params: map[string]string{"value": "gpt-5.6-luna · OpenAI"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.eng.Role(engine.RoleChat).Model; got != "gpt-5.6-luna" {
		t.Errorf("в роль легло %q — модель с таким именем не найдётся", got)
	}
	// и на экране эта роль показана выбранной, а не пустой
	doc, err := s.buildScreen("settings:models", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if dump := dumpScreen(doc); !strings.Contains(dump, "gpt-5.6-luna · OpenAI") {
		t.Error("на экране модель роли не подписана провайдером")
	}
}

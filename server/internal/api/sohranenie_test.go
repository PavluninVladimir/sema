package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// screenForms — все формы собранного экрана, включая вложенные в разделы.
func screenForms(cs []Component) []Component {
	var out []Component
	for _, к := range cs {
		if к.Type == "form" {
			out = append(out, к)
		}
		out = append(out, screenForms(к.Children)...)
	}
	return out
}

// У формы настроек ровно один способ сохраниться: либо её сохраняет
// нижняя полоса экрана, либо у неё осталась своя кнопка. Форма без
// обоих — тупик: человек правит поле, а сохранить нечем.
//
// Заодно проверяем правило, по которому они делятся: правку
// существующего («Показывать облачной модели») забирает полоса, а форма,
// заводящая новое («Добавить сервер»), свою кнопку оставляет — «Сохранить»
// внизу экрана не говорит, что сейчас появится новый сервер.
func TestSettingsFormsSavedByBar(t *testing.T) {
	s, _ := testServer(t)
	отложено, своих := 0, 0
	// корни разделов и экраны «Добавить …»: формы заведения нового стоят на своих
	// экранах, а не под списками, и правило про них то же
	screens := []string{
		entityAddr(serversScreen, serversNew, ""), entityAddr(mailScreen, mailNew, ""), entityAddr(agentsScreen, agentsNew, ""),
		entityAddr(secretsScreen, secretsNew, ""), entityAddr(modelsScreen, modelsNew, ""), entityAddr(mcpScreen, mcpNew, ""),
		entityAddr(storagesScreen, storNew, ""), entityAddr(storagesScreen, storNew, storNewDisk),
		entityAddr(skillsScreen, skillsNew, ""), entityAddr(notifySettingsScreen, notifyNew, ""),
		storagesScreen + "~" + storTabSources,
	}
	for _, в := range settingsGroups {
		screens = append(screens, в.Sections...)
	}
	for _, р := range screens {
		doc, err := s.buildScreen(р, "", "", 0)
		if err != nil {
			t.Fatalf("экран %q: %v", р, err)
		}
		for _, ф := range screenForms(doc.Components) {
			name := ф.ID
			if name == "" {
				name = ф.Title
			}
			if ф.Submit == nil {
				continue // форма без отправки — просто поля, их сохраняет что-то ещё
			}
			if ф.Deferred {
				отложено++
				if len(ф.Actions) > 0 {
					t.Errorf("%s · форма %q: полоса забрала кнопку у формы, где есть свои действия — им неоткуда уйти на сервер", р, name)
				}
				if !editsExisting(ф.Submit.Action) {
					t.Errorf("%s · форма %q: полоса сохраняет «%s», а это заведение нового", р, name, ф.Submit.Action)
				}
				continue
			}
			своих++
			if editsExisting(ф.Submit.Action) && len(ф.Actions) == 0 {
				t.Errorf("%s · форма %q правит существующее действием «%s», но кнопка осталась своей: на экране будет и полоса, и кнопка", р, name, ф.Submit.Action)
			}
		}
	}
	if отложено == 0 {
		t.Fatal("ни одной формы настроек не сохраняет полоса — её не увидит никто")
	}
	if своих == 0 {
		t.Error("ни одной формы со своей кнопкой: формы заведения нового («Добавить сервер») должны были остаться как были")
	}
}

// Разметка отложенной формы: своей кнопки нет, действие уехало скрытым
// полем, а внизу экрана стоит полоса. Без скрытого поля полоса не знала
// бы, какое действие отправлять, и сохранение молча делало бы ничего.
func TestDeferredFormMarkup(t *testing.T) {
	s, _ := testServer(t)
	// формы у «Спама»: список ящиков форм не несёт
	doc, err := s.buildScreen("settings:mail~spam", "", "", 0)
	if err != nil {
		t.Fatalf("экран: %v", err)
	}
	markup, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatalf("разметка: %v", err)
	}
	html := string(markup)
	// формы «Спама» — строки настройки, поэтому после «deferred» в классе ещё «rows»
	for _, надо := range []string{`class="form deferred rows"`, `type="hidden" name="_кнопка"`,
		`id="save-bar"`, `id="leave"`, "Отменить", "Сохранить", "Выйти без сохранения"} {
		if !strings.Contains(html, надо) {
			t.Errorf("в разметке нет %q", надо)
		}
	}
	// кнопка отправки у отложенной формы — как раз то, чего быть не должно
	if strings.Contains(html, `type="submit" name="_кнопка" value="save_`) {
		t.Error("у отложенной формы осталась своя кнопка «Сохранить» — их стало две")
	}
}

// Экран без форм настроек полосы не показывает: пустая полоса внизу
// отнимала бы место у содержимого и обещала работу, которой нет.
func TestWithoutFormsNoBar(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("health", "", "", 0)
	if err != nil {
		t.Fatalf("экран: %v", err)
	}
	if toScreen(doc).SaveBar {
		t.Error("на «Здоровье» форм настроек нет, а полоса сохранения появилась")
	}
}

// Сохранение нижней полосой отвечает «да или нет», а не экраном целиком.
//
// Форм на экране настроек несколько, и сохраняются они по одной: подмена
// экрана после первой стёрла бы правки в остальных. Отказ приходит той же
// дорогой, с кодом 200 и текстом — его показывают у своей строки, а не
// общей красной строкой наверху, где непонятно, о чём он.
func TestSavingBarAnswersYesOrNo(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()

	спросить := func(body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest("POST", "http://127.0.0.1:8765/do", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:53124"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Current-Screen", "settings:mail")
		req.Header.Set(saveHeader, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var ответ map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &ответ); err != nil {
			t.Fatalf("ответ не JSON (%d): %s", w.Code, w.Body.String())
		}
		return w.Code, ответ
	}

	код, ответ := спросить("action=save_mail_params&target=%D0%BF%D1%80%D0%BE%D0%B1%D0%B0%40%D0%BF%D1%80%D0%B8%D0%BC%D0%B5%D1%80.%D1%80%D1%84&fetch=7+%D0%B4%D0%BD%D0%B5%D0%B9")
	if код != http.StatusOK || ответ["ок"] != true {
		t.Fatalf("сохранение не прошло: %d %v", код, ответ)
	}

	код, ответ = спросить("action=такого-действия-нет&target=x")
	if код != http.StatusOK {
		t.Fatalf("отказ приехал кодом %d — клиент увёл бы человека на вход вместо строки с причиной", код)
	}
	if ответ["ок"] != false {
		t.Fatalf("неизвестное действие отчиталось успехом: %v", ответ)
	}
	if строка, _ := ответ["ошибка"].(string); строка == "" {
		t.Error("отказ без причины: у строки нечего показать")
	}
}

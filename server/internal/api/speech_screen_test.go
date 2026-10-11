package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// «Распознавание речи» на вкладке «Роли»: чем слушать — строка роли в таблице;
// чьи голосовые уходят наружу, язык, аудиофайлы — «Тонкая настройка» вкладки.
// Выбор роли — только из моделей со слухом, а остальное — обычные настройки в
// базе. Отдельного раздела под таблицей больше нет.

// roleRows — строки таблицы ролей по имени роли.
func roleRows(t *testing.T, s *Server) map[string]*Component {
	t.Helper()
	doc, err := s.buildScreen("settings:models", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]*Component{}
	for i := range doc.Components {
		walkthrough(&doc.Components[i], func(c *Component) {
			if c.Type == "row" && len(c.Cells) == 4 {
				rows[c.Subtitle] = c
			}
		})
	}
	return rows
}

func TestSpeechRoleAndFineSettingsOnRolesTab(t *testing.T) {
	s, _ := testServer(t)
	// пока моделей со слухом нет, человеку сказано, что делать, а не пустой список
	row := roleRows(t, s)[engine.RoleTranscribe]
	if row == nil || !strings.Contains(row.Caption, "Моделей со слухом пока нет") {
		t.Fatalf("без моделей со слухом: %+v", row)
	}
	// отдельного раздела речи под таблицей нет: он ушёл в таблицу и «Тонкую настройку»
	doc, _ := s.buildScreen("settings:models", "", "", 0)
	for _, c := range doc.Components {
		if c.ID == "речь" || strings.HasPrefix(c.ID, "речь-") {
			t.Errorf("остался отдельный раздел речи: %q", c.ID)
		}
	}

	if err := s.eng.AddModel(engine.ModelConf{Name: "слух", Type: "openai",
		BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini-transcribe"}); err != nil {
		t.Fatal(err)
	}
	row = roleRows(t, s)[engine.RoleTranscribe]
	sel := row.Cells[0].Select
	if sel == nil || sel.Action.Action != "set_role_model" || sel.Action.Target != engine.RoleTranscribe {
		t.Fatalf("выбор модели: %+v", sel)
	}
	if sel.OptionsRef != "слышащие" || row.Cells[1].Select.OptionsRef != "слышащие" {
		t.Fatalf("у роли речи списки не из слышащих: %q %q", sel.OptionsRef, row.Cells[1].Select.OptionsRef)
	}
	// в наборе только «нет» и модель со слухом; обычная «тестовая» — не предлагается
	doc, _ = s.buildScreen("settings:models", "", "", 0)
	if got := strings.Join(doc.OptionSets["слышащие"], "|"); !strings.HasPrefix(got, "нет|слух") || strings.Contains(got, "тестовая") {
		t.Fatalf("набор для роли речи: %q", got)
	}
	// умолчание — самая дешёвая из умеющих
	if !strings.HasPrefix(sel.Value, "слух") {
		t.Fatalf("значение: %q", sel.Value)
	}

	// параметры — три формы «Тонкой настройки», у каждой действие прежнего списка
	fold := rareFoldOf(t, s, "settings:models")
	if fold.Meta != "всё по умолчанию" || !strings.Contains(fold.Caption, "распознавание речи") {
		t.Errorf("«Тонкая настройка»: %q · %q", fold.Caption, fold.Meta)
	}
	forms := map[string]*Component{}
	for _, id := range []string{"speech-outside", "speech-language", "speech-files"} {
		var found *Component
		body := rareBody(t, s, "settings:models")
		for i := range body {
			walkthrough(&body[i], func(c *Component) {
				if c.ID == id {
					found = c
				}
			})
		}
		if found == nil || len(found.Fields) != 1 || found.Fields[0].Name != "value" || found.Submit == nil {
			t.Fatalf("формы %s нет или она не из одного поля: %+v", id, found)
		}
		if found.Fields[0].Default == nil || found.Fields[0].Value != *found.Fields[0].Default {
			t.Errorf("%s: на пустой базе значение %q при умолчании %v", id, found.Fields[0].Value, found.Fields[0].Default)
		}
		forms[id] = found
	}
	if f := forms["speech-outside"]; f.Fields[0].Value != "Только мои" || f.Submit.Action != "save_speech_outside" ||
		strings.Join(f.Fields[0].Options, "|") != "Только мои|Все голосовые|Никакие" {
		t.Fatalf("чьи голосовые: %+v", f)
	}
	if f := forms["speech-language"]; f.Fields[0].Value != "Русский" || f.Submit.Action != "save_speech_language" {
		t.Fatalf("язык: %+v", f)
	}
	if f := forms["speech-files"]; f.Fields[0].Value != "Загруженные мной" || f.Submit.Action != "save_speech_files" {
		t.Fatalf("файлы: %+v", f)
	}
	// правка меняет подпись «Тонкой настройки»: умолчание считается по реестру
	if _, err := s.execute(actionRequest{Action: "save_speech_outside", Params: map[string]string{"value": "Никакие"}}); err != nil {
		t.Fatal(err)
	}
	if fold := rareFoldOf(t, s, "settings:models"); fold.Meta != "1 изменён" {
		t.Errorf("после правки подпись %q", fold.Meta)
	}
}

// Вкладка «Роли»: у роли речи в списках только модели со слухом, у остальных
// — все; наборы вариантов отдаются один раз на документ.
func TestSpeechRoleOnRolesTab(t *testing.T) {
	s, _ := testServer(t)
	if err := s.eng.AddModel(engine.ModelConf{Name: "слух", Type: "openai",
		BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini-transcribe"}); err != nil {
		t.Fatal(err)
	}
	rows := roleRows(t, s)
	speech, chat := rows[engine.RoleTranscribe], rows[engine.RoleChat]
	if speech == nil || chat == nil {
		t.Fatalf("в таблице нет ролей речи и чатов: %v", rows)
	}
	if len(speech.Cells) != 4 || speech.Cells[0].Select.OptionsRef != "слышащие" || speech.Cells[1].Select.OptionsRef != "слышащие" {
		t.Errorf("списки роли речи: %+v", speech.Cells)
	}
	if chat.Cells[0].Select.OptionsRef != "модели" || chat.Cells[1].Select.OptionsRef != "запасные" {
		t.Errorf("у обычной роли наборы %q и %q", chat.Cells[0].Select.OptionsRef, chat.Cells[1].Select.OptionsRef)
	}
	if !strings.Contains(speech.Caption, "модели со слухом") {
		t.Errorf("подпись роли речи не говорит про слух: %q", speech.Caption)
	}
}

// Действия: выбор роли проверяет умение, остальное пишется в настройки.
func TestSpeechActions(t *testing.T) {
	s, st := testServer(t)
	if err := s.eng.AddModel(engine.ModelConf{Name: "слух", Type: "openai",
		BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini-transcribe"}); err != nil {
		t.Fatal(err)
	}
	act := func(action, target, value string) error {
		_, err := s.execute(actionRequest{Action: action, Target: target, Params: map[string]string{"value": value}})
		return err
	}
	// «тестовая» — обычная модель: на роль речи не встаёт
	if err := act("set_role_model", engine.RoleTranscribe, "тестовая · на этой машине"); err == nil ||
		!strings.Contains(err.Error(), "не слышит речь") {
		t.Fatalf("обычная модель встала на роль речи: %v", err)
	}
	if err := act("set_role_model", engine.RoleTranscribe, "слух · OpenAI"); err != nil {
		t.Fatalf("модель со слухом не встала: %v", err)
	}
	if got := s.eng.Role(engine.RoleTranscribe).Model; got != "слух" {
		t.Fatalf("роль: %q", got)
	}

	if err := act("save_speech_outside", "", "Все голосовые"); err != nil || s.eng.SpeechOutside() != "all" {
		t.Fatalf("all: %v %q", err, s.eng.SpeechOutside())
	}
	if err := act("save_speech_outside", "", "Никакие"); err != nil || s.eng.SpeechOutside() != "never" {
		t.Fatalf("never: %v %q", err, s.eng.SpeechOutside())
	}
	if err := act("save_speech_outside", "", "Только мои"); err != nil || s.eng.SpeechOutside() != "own" {
		t.Fatalf("own: %v %q", err, s.eng.SpeechOutside())
	}
	if err := act("save_speech_language", "", "Английский"); err != nil || s.eng.SpeechLanguage() != "en" {
		t.Fatalf("en: %v %q", err, s.eng.SpeechLanguage())
	}
	if err := act("save_speech_language", "", "Пусть определит модель"); err != nil || s.eng.SpeechLanguage() != "" {
		t.Fatalf("авто: %v %q", err, s.eng.SpeechLanguage())
	}
	// выбор без языка не возвращается к «ru» сам: пустая настройка — решение человека
	if s.eng.SpeechLanguage() != "" || st.Setting("speech.language", "x") != "" {
		t.Fatal("пустой язык не сохранился как пустой")
	}
	if err := act("save_speech_files", "", "Не расшифровывать"); err != nil || s.eng.SpeechFiles() != "off" {
		t.Fatalf("off: %v %q", err, s.eng.SpeechFiles())
	}

	// умение по названию не угадать — отмечает человек, и только у неречевых по имени
	if err := s.eng.AddModel(engine.ModelConf{Name: "мой-сервер", Type: "openai",
		BaseURL: "http://192.168.1.5:9000/v1", Model: "large-v3"}); err != nil {
		t.Fatal(err)
	}
	if err := act("toggle_model_hears", "мой-сервер", ""); err != nil || !s.eng.ModelHears("мой-сервер") {
		t.Fatalf("отметка: %v", err)
	}
	if err := act("toggle_model_hears", "мой-сервер", ""); err != nil || s.eng.ModelHears("мой-сервер") {
		t.Fatalf("снятие отметки: %v", err)
	}
	// у gpt-4o-mini-transcribe умение видно по названию: снимать нечего
	if err := act("toggle_model_hears", "слух", ""); err != nil || !s.eng.ModelHears("слух") {
		t.Fatalf("умение по названию снято: %v", err)
	}
}

package api

import "strings"

// «Распознавание речи» на вкладке «Роли»: чем слушать голосовые и аудиофайлы
// (строка роли в таблице), чьи голосовые вообще можно отдавать облачной модели,
// на каком языке слушать, какие файлы расшифровывать («Тонкая настройка»
// вкладки). Словарь UI-DSL не менялся.

// Чьи голосовые уходят облачной модели. До распознавания неизвестно, что в
// записи сказано, поэтому мерка — не содержание, а чей голос.
var speechOutsideModes = []struct{ Key, Label string }{
	{"own", "Только мои"},
	{"all", "Все голосовые"},
	{"never", "Никакие"},
}

// Какие аудиофайлы расшифровываются сами. Записи из почты и папок — никогда:
// это деньги и чужие голоса; по просьбе в чате их расшифровывает Сёма.
var speechFilesModes = []struct{ Key, Label string }{
	{"upload", "Загруженные мной"},
	{"off", "Не расшифровывать"},
}

// Язык, на котором слушать. Пустой код — модель определит сама.
var speechLanguages = []struct{ Code, Label string }{
	{"ru", "Русский"},
	{"en", "Английский"},
	{"uk", "Украинский"},
	{"", "Пусть определит модель"},
}

func modeOptions(modes []struct{ Key, Label string }) []string {
	out := make([]string, 0, len(modes))
	for _, m := range modes {
		out = append(out, m.Label)
	}
	return out
}

func modeLabel(modes []struct{ Key, Label string }, key string) string {
	for _, m := range modes {
		if m.Key == key {
			return m.Label
		}
	}
	return modes[0].Label
}

func modeKey(modes []struct{ Key, Label string }, label string) string {
	for _, m := range modes {
		if m.Label == label {
			return m.Key
		}
	}
	return modes[0].Key
}

// languageLabel — подпись языка по коду; неизвестный код показывается как есть,
// чтобы выбор человека не терялся из списка.
func languageLabel(code string) string {
	for _, l := range speechLanguages {
		if l.Code == code {
			return l.Label
		}
	}
	return code
}

func languageOptions(current string) []string {
	out := make([]string, 0, len(speechLanguages)+1)
	known := false
	for _, l := range speechLanguages {
		out = append(out, l.Label)
		known = known || l.Code == current
	}
	if !known {
		out = append(out, current)
	}
	return out
}

func languageCode(label string) string {
	for _, l := range speechLanguages {
		if l.Label == label {
			return l.Code
		}
	}
	return strings.ToLower(strings.TrimSpace(label)) // свой код, оставшийся в списке
}

// speechFine — параметры распознавания речи в «Тонкой настройке» вкладки «Роли»:
// чьи голосовые уходят облачной модели, язык, аудиофайлы. Сама роль — строка
// таблицы; параметры правят редко, и под рукой им делать нечего. Каждая — своя
// форма с одним полем `value`: действия те же, что у прежних списков в строках
// (`save_speech_outside`, `save_speech_language`, `save_speech_files`), а
// умолчание берётся из реестра — подпись «всё по умолчанию» считается от него.
func (s *Server) speechFine(add func(Component)) {
	form := func(id, action, label, hint string, f Field, def string) Component {
		f.Name, f.Label, f.Hint = "value", label, hint
		f.Default = &def
		// ключ в реестре: «speech-outside» → «speech.outside». Поле здесь собрано не
		// settingField (значение — подпись варианта, а не строка настройки), и без
		// ключа индекс настроек и журнал не узнали бы, чьё это поле
		f.key = "speech." + strings.TrimPrefix(id, "speech-")
		return Component{Type: "form", ID: id, Style: formRows, Fields: []Field{f},
			Submit: &Action{Action: action, Label: "Сохранить"}}
	}
	lang := s.eng.SpeechLanguage()
	add(Component{Type: "section", ID: "speech-settings", Title: "Распознавание речи", Tier: tierRare, Children: []Component{
		form("speech-outside", "save_speech_outside", "Чьи голосовые уходят облачной модели",
			"До распознавания неизвестно, что в записи сказано, поэтому решает не содержание, а чей голос. "+
				"«Только мои» — голосовые из вашего админ-чата в Telegram и файлы, которые вы загрузили; голосовые из чужих чатов "+
				"облачной модели не уходят, а в переписке остаётся «голосовое, не распознано». Своя доверенная модель слушает всё. "+
				"Распознанный текст дальше живёт по обычным правилам «секретного» и «чувствительного».",
			Field{Type: "segments", Options: modeOptions(speechOutsideModes),
				Value: modeLabel(speechOutsideModes, s.eng.SpeechOutside())},
			modeLabel(speechOutsideModes, settingDef("speech.outside").Default)),
		form("speech-language", "save_speech_language", "Язык распознавания",
			"Подсказка модели: с языком она ошибается заметно реже. Русский — по умолчанию; «пусть определит модель» — осознанный выбор, а не сброс",
			Field{Type: "picker", Options: languageOptions(lang), Value: languageLabel(lang)},
			languageLabel(settingDef("speech.language").Default)),
		form("speech-files", "save_speech_files", "Аудиофайлы",
			"Загруженная запись получает текст, как картинка — текст OCR. Записи из почты и папок Сёма сам не расшифровывает: "+
				"это деньги и чужие голоса. Такую запись он расшифрует, если попросить в чате.",
			Field{Type: "segments", Options: modeOptions(speechFilesModes),
				Value: modeLabel(speechFilesModes, s.eng.SpeechFiles())},
			modeLabel(speechFilesModes, settingDef("speech.files").Default)),
	}})
}

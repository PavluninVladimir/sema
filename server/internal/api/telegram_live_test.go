package api

// Настройки живого разговора в Telegram: «прочитано», «печатает», вид
// потока ответа и его пределы — видны на вкладке «Ответы и пределы» и
// меняются действиями; неверное значение не принимается.

import (
	"strings"
	"testing"
)

func TestTelegramLiveSettingsChange(t *testing.T) {
	s, st := telegramWithChannels(t, 0)
	screen := tgScreen + "~" + tgTabAnswer
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// пределы потока — поля «Тонкой настройки» вкладки, а не строки с вопросом
	dump := dumpWithRare(t, s, screen, doc)
	for _, want := range []string{"Отмечать прочитанным в вашем разговоре с Сёмой", "Отмечать прочитанным в чатах, где Сёма отвечает сам",
		"Показывать «печатает…»", "Как приходит ответ",
		"Как часто дописывать ответ", "Сколько знаков до первого сообщения"} {
		if !strings.Contains(dump, want) {
			t.Errorf("на вкладке нет строки %q", want)
		}
	}

	// выключатель: по умолчанию включён, нажатие выключает, повторное — возвращает
	if st.Setting("tg.typing", "1") != "1" {
		t.Fatal("«печатает» по умолчанию должно быть включено")
	}
	if _, err := s.execute(actionRequest{Action: "tg_toggle_flag", Target: "tg.typing"}); err != nil {
		t.Fatal(err)
	}
	if st.Setting("tg.typing", "1") != "0" {
		t.Fatal("выключатель не выключил")
	}
	if _, err := s.execute(actionRequest{Action: "tg_toggle_flag", Target: "tg.typing"}); err != nil {
		t.Fatal(err)
	}
	if st.Setting("tg.typing", "1") != "1" {
		t.Fatal("выключатель не вернул")
	}
	if _, err := s.execute(actionRequest{Action: "tg_toggle_flag", Target: "tg.history"}); err == nil {
		t.Fatal("чужая настройка принята за выключатель")
	}

	// «прочитано» в чатах с автоответом: по умолчанию выключено, а не включено,
	// как остальные; нажатие включает
	row := findByID(doc.Components, "tg-flag-tg.read_mark_reply")
	if row == nil || row.Toggle != "off" {
		t.Fatalf("отметка в автоответах должна быть выключена по умолчанию: %+v", row)
	}
	if _, err := s.execute(actionRequest{Action: "tg_toggle_flag", Target: "tg.read_mark_reply"}); err != nil {
		t.Fatal(err)
	}
	if st.Setting("tg.read_mark_reply", "0") != "1" {
		t.Fatal("выключатель отметки в автоответах не включил")
	}
	doc, err = s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if row := findByID(doc.Components, "tg-flag-tg.read_mark_reply"); row == nil || row.Toggle != "on" {
		t.Fatalf("после нажатия тумблер не включён: %+v", row)
	}
	if _, err := s.execute(actionRequest{Action: "tg_toggle_flag", Target: "tg.read_mark_reply"}); err != nil {
		t.Fatal(err)
	}
	if st.Setting("tg.read_mark_reply", "0") != "0" {
		t.Fatal("повторное нажатие не выключило")
	}

	// вид потока: по слову для человека, а не по значению
	if _, err := s.execute(actionRequest{Action: "tg_set_stream", Params: map[string]string{"value": "Одним куском, когда готово"}}); err != nil {
		t.Fatal(err)
	}
	if got := st.Setting("tg.stream", ""); got != "off" {
		t.Fatalf("tg.stream = %q", got)
	}
	if _, err := s.execute(actionRequest{Action: "tg_set_stream", Params: map[string]string{"value": "как-нибудь"}}); err == nil {
		t.Fatal("неизвестный вид ответа принят")
	}

	// пределы: ритм не быстрее допустимого, первое сообщение не пустое
	if _, err := s.execute(actionRequest{Action: "tg_set_limit", Target: "tg.stream_ms", Params: map[string]string{"value": "50"}}); err == nil {
		t.Fatal("ритм быстрее ста миллисекунд принят")
	}
	if _, err := s.execute(actionRequest{Action: "tg_set_limit", Target: "tg.stream_ms", Params: map[string]string{"value": "800"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "tg_set_limit", Target: "tg.stream_first", Params: map[string]string{"value": "0"}}); err == nil {
		t.Fatal("нулевой порог первого сообщения принят")
	}
	if st.Setting("tg.stream_ms", "") != "800" {
		t.Fatal("допустимый ритм не сохранился")
	}
}

// Отказ Telegram в живом черновике виден на экране, и его можно снять: выбор
// «черновик» заново или кнопка «Пробовать снова».
func TestTelegramDraftRefusalShownAndCleared(t *testing.T) {
	s, st := telegramWithChannels(t, 0)
	_ = st.SetSetting("tg.stream", "draft")
	_ = st.SetSetting(tgDraftOffPrefix+telegramAccount, "SEND_MESSAGE_TYPE_INVALID")
	doc, err := s.buildScreen(tgScreen+"~"+tgTabAnswer, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	if !strings.Contains(dump, "аккаунт не принимает: SEND_MESSAGE_TYPE_INVALID") || !strings.Contains(dump, "Пробовать снова") {
		t.Fatalf("отказ не виден на экране:\n%s", dump)
	}
	if _, err := s.execute(actionRequest{Action: "tg_draft_retry", Target: tgDigits}); err != nil {
		t.Fatal(err)
	}
	if st.Setting(tgDraftOffPrefix+telegramAccount, "") != "" {
		t.Fatal("отказ не снят")
	}
}

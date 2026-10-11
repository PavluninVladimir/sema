package max

import (
	"encoding/json"
	"testing"
)

// Push с новым сообщением узнаётся по содержимому: опкод в описании
// протокола не закреплён, а чат и сообщение в кадре есть всегда.
func TestUpdateOf(t *testing.T) {
	ok := `{"chatId":7268926,"message":{"id":116762203424780659,"time":1781704393993,
		"text":"Привет!","sender":3260455,"type":"USER","attaches":[]}}`
	u, got := updateOf(json.RawMessage(ok))
	if !got {
		t.Fatal("сообщение не распознано")
	}
	if u.ChatID != 7268926 || u.MsgID != 116762203424780659 || u.Text != "Привет!" || u.Sender != 3260455 {
		t.Fatalf("разобрали не то: %+v", u)
	}

	// печатает, читает, удалил — не сообщения
	for _, raw := range []string{
		`{"chatId":7268926}`,
		`{"chatId":7268926,"message":{"id":5,"deleted":"USER"}}`,
		`{"message":{"id":5,"text":"без чата"}}`,
		`не json`,
	} {
		if _, got := updateOf(json.RawMessage(raw)); got {
			t.Errorf("приняли за сообщение: %s", raw)
		}
	}
}

// Ошибка сервера должна доходить словами: у MAX причина лежит в трёх
// разных полях, и пустой отказ тоже бывает.
func TestErrorOf(t *testing.T) {
	cases := map[string]string{
		`{"localizedMessage":"Пользователь заблокирован"}`: "MAX: Пользователь заблокирован",
		`{"error":"auth.invalid.code"}`:                    "MAX: auth.invalid.code",
		`{"message":"Key: error.user"}`:                    "MAX: Key: error.user",
		`{}`:                                               "MAX отказал без объяснения",
	}
	for raw, want := range cases {
		if got := errorOf(json.RawMessage(raw)).Error(); got != want {
			t.Errorf("%s: получили %q, ждали %q", raw, got, want)
		}
	}
}

func TestKindOf(t *testing.T) {
	for in, want := range map[string]string{
		"DIALOG": "dialog", "CHANNEL": "channel", "CHAT": "chat", "": "chat",
	} {
		if got := kindOf(in); got != want {
			t.Errorf("%q: получили %q, ждали %q", in, got, want)
		}
	}
}

// MAX присылает id то числом, то строкой: у сообщений истории строкой, у
// чатов числом. Оба вида должны читаться, иначе история молча пустая.
func TestFlexID(t *testing.T) {
	raw := `{"chatId":-75531266282031,"message":{"id":"116762203424780659","text":"привет","sender":"3260455","attaches":[]}}`
	u, ok := updateOf(json.RawMessage(raw))
	if !ok {
		t.Fatal("сообщение со строковым id не распозналось")
	}
	if u.ChatID != -75531266282031 || u.MsgID != 116762203424780659 || u.Sender != 3260455 {
		t.Fatalf("разобрали не то: %+v", u)
	}
}

// Токен фотографии из ответа хранилища MAX; нет его — пусто, а не паника.
func TestPhotoToken(t *testing.T) {
	if got := photoTokenOf([]byte(`{"photos":{"123":{"token":"abc"}}}`)); got != "abc" {
		t.Fatalf("токен не вынут: %q", got)
	}
	if got := photoTokenOf([]byte(`{"error":"x"}`)); got != "" {
		t.Fatalf("токен из воздуха: %q", got)
	}
	if got := replyShape([]byte(`{"b":1,"a":"секрет"}`)); got != "поля a, b" {
		t.Fatalf("форма ответа: %q", got)
	}
}

// Голосовое — вложение AUDIO с адресом. Имена полей кадра запоминаются
// (значения — никогда): по ним видно, где искать адрес, когда живой кадр
// окажется не таким, как ждали.
func TestVoiceAttachIsRecognized(t *testing.T) {
	raw := `{"chatId":9,"message":{"id":5,"text":"","sender":1,"attaches":[
		{"_type":"AUDIO","url":"https://cdn.example/a.ogg","duration":3000,"audioId":77,"token":"секрет"},
		{"_type":"PHOTO","baseUrl":"https://cdn.example/p.jpg"},
		{"_type":"UNSUPPORTED","url":"https://cdn.example/x"},
		{"_type":"UNSUPPORTED"}]}}`
	u, ok := updateOf(json.RawMessage(raw))
	if !ok || len(u.Files) != 4 {
		t.Fatalf("вложений %d: %+v", len(u.Files), u)
	}
	audio := u.Files[0]
	if !audio.Voice() || audio.URL != "https://cdn.example/a.ogg" || audio.Human() != "голосовое сообщение" {
		t.Fatalf("AUDIO: %+v", audio)
	}
	if got := len(audio.Keys); got != 5 || audio.Keys[0] != "_type" {
		t.Fatalf("имена полей: %v", audio.Keys)
	}
	for _, k := range audio.Keys {
		if k == "секрет" {
			t.Fatal("в именах полей оказалось значение")
		}
	}
	if u.Files[1].Voice() {
		t.Error("фотография принята за голосовое")
	}
	// вид не назван, но адрес есть — как голосовое из прежнего разбора; без адреса — нет
	if !u.Files[2].Voice() || u.Files[3].Voice() {
		t.Errorf("UNSUPPORTED: с адресом %v, без адреса %v", u.Files[2].Voice(), u.Files[3].Voice())
	}
}

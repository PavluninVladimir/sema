package engine

import (
	"strings"
	"testing"
)

// Адрес эмбеддера, заданный человеком, переживает перезапуск.
//
// До 10 сентября 2026 при старте он брался только из файла запуска.
// Человек менял его на «Инструментах», клиент поднимался с новым
// адресом — и следующий же перезапуск возвращал прежний, молча. На
// живой Сёме в config.yaml стоял «http://embed:8080», такой машины в
// сети нет, и векторов не строилось вовсе: в журнале тысячи строк
// «lookup embed: no such host», а поиск по смыслу не работал.
func TestAddrEmbedderSurvivesRestart(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting(embedURLKey, "http://127.0.0.1:1234/v1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(embedModelKey, "text-embedding-bge-m3"); err != nil {
		t.Fatal(err)
	}
	// перезапуск: тот же каталог, тот же файл запуска
	second, err := New(e.conf, st)
	if err != nil {
		t.Fatal(err)
	}
	if second.embedder == nil {
		t.Fatal("после перезапуска эмбеддера нет вовсе")
	}
	if got := second.EmbedBaseURL(); !strings.Contains(got, "127.0.0.1:1234") {
		t.Errorf("адрес человека потерян: %q", got)
	}
	if got := second.embedder.BaseURL; !strings.Contains(got, "127.0.0.1:1234") {
		t.Errorf("клиент поднят не по тому адресу: %q", got)
	}
}

package engine

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Спам провайдера и корзина читаются НА МЕСТЕ. Это главное условие всей
// правки: решение о них принял почтовый сервер или сам человек, и Сёма
// его не оспаривает — он не помечает письма прочитанными и никуда их не
// переносит. Иначе Сёма воевал бы с чужим ящиком.
func TestForeignFoldersOnlyRead(t *testing.T) {
	cases := map[string]string{
		"[Gmail]/Спам":      mail.FolderJunk,
		"Корзина":           mail.FolderTrash,
		"[Gmail]/Вся почта": mail.FolderAll,
	}
	for name, kind := range cases {
		if !readOnPlace(false, false, kind) {
			t.Errorf("%q: Сёма собрался переносить письма из чужой папки", name)
		}
	}
	// обычная папка ящика по-прежнему разбирается с переносом в архив
	if readOnPlace(false, false, "") {
		t.Error("обычная папка вдруг стала читаться на месте")
	}
	// прежние два повода читать на месте никуда не делись
	if !readOnPlace(true, false, "") {
		t.Error("режим «только читать» перестал работать")
	}
	if !readOnPlace(false, true, "") {
		t.Error("«Отправленные» перестали читаться на месте")
	}
}

// У писем из чужих папок своё состояние, а не «archived».
func TestOfForeignMessagesOwnState(t *testing.T) {
	if got := folderStatus(mail.FolderJunk); got != store.StatusServerSpam {
		t.Errorf("спам провайдера: %q", got)
	}
	if got := folderStatus(mail.FolderTrash); got != store.StatusServerTrash {
		t.Errorf("корзина: %q", got)
	}
	// «вся почта» — это обычные письма ящика, просто мимо «Входящих»
	if got := folderStatus(mail.FolderAll); got != store.StatusArchived {
		t.Errorf("вся почта: %q", got)
	}
	if got := folderStatus(""); got != store.StatusArchived {
		t.Errorf("обычная папка: %q", got)
	}
}

// Сводка «Почты» собирается по списку состояний. Попади туда спам
// провайдера — и «что пришло сегодня» утонуло бы в чужом спаме: у одного
// живого ящика его сорок одно письмо против четырёх настоящих.
func TestForeignSpamNotReachesInSummary(t *testing.T) {
	for _, s := range digestStatuses {
		if s == store.StatusServerSpam || s == store.StatusServerTrash {
			t.Fatalf("состояние %q попало в сводку «Почты»", s)
		}
	}
	// а «вся почта» — попадает: это обычные письма ящика
	found := false
	for _, s := range digestStatuses {
		if s == store.StatusArchived {
			found = true
		}
	}
	if !found {
		t.Error("разобранные из папок письма пропали из сводки")
	}
}

// Выключатель есть и по умолчанию включён: до 10 сентября 2026 «письмо
// не нашлось» значило «письма нет».
func TestReadForeignFoldersEnabledByDefault(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if !e.ReadServerFolders() {
		t.Error("по умолчанию Сёма снова слеп к спаму провайдера")
	}
	if err := e.SetReadServerFolders(false); err != nil {
		t.Fatal(err)
	}
	if e.ReadServerFolders() {
		t.Error("выключатель не выключает")
	}
}

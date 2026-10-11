package engine

import (
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Письмо, уехавшее в архив мимо разбора, разбирается задним числом — но
// никуда не переносится: оно уже в архиве на сервере, и суд задним числом
// даёт суждение, а не действие.
func TestPreCourtNotMovesMessages(t *testing.T) {
	judge := &fakeProvider{answer: `{"spam": true, "confidence": 0.93, "reason": "рассылка магазина"}`}
	e, st := newTestEngine(t, "судья", judge)
	assignRole(t, e, RoleFilter, "судья")

	// три письма: одно мимо разбора, одно уже рассуженное, одно из папки
	// человека — досуд берёт только первое
	bypasses := messageInArchive(t, st, "<мимо@т>", "Счет от АО «Горэнерго»", "папка: INBOX", 0)
	judged := messageInArchive(t, st, "<судили@т>", "Уже разобрано", "модель: рассылка", 0.88)
	fromFolders := messageInArchive(t, st, "<папка@т>", "Из папки «Счета»", "папка: Счета", 0)

	list, err := st.MessagesUnjudged(100)
	if err != nil {
		t.Fatal(err)
	}
	// только «Входящие». Первый заход брал всё с причиной «папка: …» — то
	// есть и письма, которые человек своими руками разложил по папкам. На
	// живой Сёме так набралось 448 писем вместо 41, и шестнадцать старых
	// рассылок внезапно потребовали решения
	if len(list) != 1 {
		t.Fatalf("к досуду отобрано %d писем, а из «Входящих» одно: %v", len(list), list)
	}
	if has(list, fromFolders) {
		t.Error("письмо из папки человека взято на пересуд: папка и есть его решение")
	}
	if has(list, judged) {
		t.Error("рассуженное письмо взято на второй суд")
	}

	// сам суд
	if err := e.handleMsgJudge(t.Context(), &store.WorkItem{Kind: WorkMsgJudge, Ref: itoa64(bypasses)}); err != nil {
		t.Fatal(err)
	}
	m, err := st.Message(bypasses)
	if err != nil {
		t.Fatal(err)
	}
	// вердикт «спам» — это «ждёт решения», а не перенос: переносит человек
	if m.Status != store.StatusPending {
		t.Errorf("статус %q, а спам должен ждать решения человека", m.Status)
	}
	if m.Status == store.StatusAutoMoved {
		t.Error("Сёма перенёс письмо сам — необратимое без кнопки человека")
	}
	if m.Confidence == 0 {
		t.Error("уверенность не записана: статус без основания — это «решил, а почему, не скажу»")
	}
	if !strings.Contains(m.Reason, "задним числом") {
		t.Errorf("причина не говорит, что суд был поздним: %q", m.Reason)
	}

	// второй раз то же письмо не судим
	m2before := m.Reason
	if err := e.handleMsgJudge(t.Context(), &store.WorkItem{Kind: WorkMsgJudge, Ref: itoa64(bypasses)}); err != nil {
		t.Fatal(err)
	}
	if m3, _ := st.Message(bypasses); m3.Reason != m2before {
		t.Errorf("письмо рассудили дважды: %q → %q", m2before, m3.Reason)
	}
}

// Разовый проход ставится один раз: метка в настройках, как у остальных
// переездов. Иначе каждый запуск Сёмы гнал бы очередь по кругу.
func TestPreCourtInstallsOnce(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	// движок уже прошёл этот путь при создании — метка стоит. Снимаем её:
	// нам нужно первое утро Сёмы, а не пятое
	if err := st.SetSetting(preCourtKey, ""); err != nil {
		t.Fatal(err)
	}
	messageInArchive(t, st, "<раз@т>", "Счёт", "папка: INBOX", 0)
	if n := e.RejudgeUnjudged(); n != 1 {
		t.Fatalf("поставлено %d, а письмо одно", n)
	}
	messageInArchive(t, st, "<два@т>", "Ещё счёт", "папка: INBOX", 0)
	if n := e.RejudgeUnjudged(); n != 0 {
		t.Errorf("разовый проход пошёл по второму кругу: %d", n)
	}
}

// assignRole — роль «Фильтр писем» на поддельную модель: без этого
// classify честно скажет «провайдер не настроен» и ничего не рассудит.
func assignRole(t *testing.T, e *Engine, role, model string) {
	t.Helper()
	roles := e.Roles()
	for i := range roles {
		if roles[i].Name == role {
			roles[i].Model, roles[i].Spare = model, "нет"
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

func messageInArchive(t *testing.T, st *store.Store, id, subject, reason string, conf float64) int64 {
	t.Helper()
	m := &store.Message{MessageID: id, Subject: subject, FromAddr: "billing@пример.рф",
		Status: store.StatusArchived, Reason: reason, Confidence: conf}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func has(list []int64, id int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

// Уборка за широким досудом: тронутые письма возвращаются в архив, а
// ошибочно поставленные работы снимаются с очереди.
func TestCancelWidePreCourt(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	// письмо, которое первый досуд успел объявить спамом
	touched := messageInArchive(t, st, "<тронуто@т>", "Старая рассылка",
		"модель: рассылка магазина · разобрано задним числом", 0.95)
	if err := st.SetStatusFull(touched, store.StatusPending,
		"модель: рассылка магазина · разобрано задним числом", 0.95); err != nil {
		t.Fatal(err)
	}
	// и работа в очереди, поставленная по ошибке
	if err := st.Enqueue(WorkMsgJudge, "999", "", 4); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("mail.rejudge_wide_undone", ""); err != nil {
		t.Fatal(err)
	}

	if n := e.CancelWidePreCourt(); n != 1 {
		t.Fatalf("возвращено %d писем, а тронуто было одно", n)
	}
	m, err := st.Message(touched)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != store.StatusArchived {
		t.Errorf("письмо осталось в состоянии %q", m.Status)
	}
	if m.Confidence != 0 {
		t.Errorf("уверенность не снята: %.2f", m.Confidence)
	}
	// причина написана так, как её прочтёт человек в графе «Почему», а не
	// служебной запиской самому себе
	if !strings.Contains(m.Reason, "разбора не было") {
		t.Errorf("причина не объясняет состояние письма: %q", m.Reason)
	}
	if strings.Contains(m.Reason, "ошибочн") || strings.Contains(m.Reason, "возврат") {
		t.Errorf("в графе «Почему» остался след нашей уборки: %q", m.Reason)
	}
	// второй раз уборка не идёт: метка стоит
	if n := e.CancelWidePreCourt(); n != 0 {
		t.Errorf("уборка пошла по второму кругу: %d", n)
	}
}

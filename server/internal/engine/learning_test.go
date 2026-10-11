package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Самообучение: черновик не теряется при перезапуске, отклонённое не
// повторяется, решение всегда за человеком.

func TestWorkScore(t *testing.T) {
	easy := []*store.ChatMsg{{Role: "user", Content: "привет"}, {Role: "assistant", Content: "здравствуйте"}}
	if score, _ := workScore(easy); score >= 5 {
		t.Errorf("простой разговор набрал %d очков", score)
	}
	hard := []*store.ChatMsg{
		{Role: "user", Content: "обнови сервер"},
		{Role: "assistant", Content: "```sema-ssh:prod\napt-get update```"},
		{Role: "assistant", Content: "ошибка 404, индекс устарел"},
		{Role: "assistant", Content: "```sema-ssh:prod\napt-get upgrade```"},
		{Role: "assistant", Content: "готово"},
	}
	score, why := workScore(hard)
	if score < 5 {
		t.Errorf("непростая работа набрала всего %d очков (%s)", score, why)
	}
	if !strings.Contains(why, "команд") {
		t.Errorf("объяснение без команд: %q", why)
	}
}

// Черновик переживает перезапуск: решение читается из базы, а не из памяти.
func TestDraftSurvivesRestart(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	d := &store.SkillDraft{ID: "проба-1", Name: "linux-admin", Title: "Linux-сервер",
		Text: "Сначала apt-get update.", IsNew: true, Kind: store.DraftExperience, Who: "работа «обновление»"}
	if err := st.AddSkillDraft(d); err != nil {
		t.Fatalf("черновик: %v", err)
	}
	// «перезапуск»: новый движок на той же базе
	again, err := New(e.conf, st)
	if err != nil {
		t.Fatalf("движок: %v", err)
	}
	if err := again.DecideSkill("проба-1", true); err != nil {
		t.Fatalf("решение после перезапуска: %v", err)
	}
	k, ok := st.Skill("linux-admin")
	if !ok || !strings.Contains(k.Text, "apt-get update") {
		t.Fatalf("принятый черновик не стал скиллом: %+v", k)
	}
	got, _ := st.SkillDraft("проба-1")
	if got.State != store.DraftAccepted {
		t.Errorf("состояние черновика: %q", got.State)
	}
}

// Отклонённое не предлагается снова.
func TestRejectedDraftIsNotProposedAgain(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	text := "Никогда не перезагружать сервер без спроса."
	d := &store.SkillDraft{ID: "проба-2", Name: "linux-admin", Title: "Linux-сервер", Text: text, IsNew: true}
	if err := st.AddSkillDraft(d); err != nil {
		t.Fatal(err)
	}
	if err := e.DecideSkill("проба-2", false); err != nil {
		t.Fatalf("отклонение: %v", err)
	}
	if !st.DraftRejected(store.DraftPrint("linux-admin", text)) {
		t.Fatal("отпечаток отклонённого не запомнен")
	}
	// то же самое, набранное иначе, тоже считается отклонённым
	if !st.DraftRejected(store.DraftPrint("Linux-Admin", "Никогда   не перезагружать\nсервер без спроса.")) {
		t.Error("тот же смысл с другими пробелами прошёл как новый")
	}
	// и предложение не создаётся
	proposed := &store.Skill{Name: "linux-admin", Title: "Linux-сервер", Text: text}
	if err := e.proposeSkillDraft(t.Context(), proposed, store.DraftExperience, "работа", "", 0); err != nil {
		t.Fatalf("предложение: %v", err)
	}
	open, _ := st.SkillDrafts(store.DraftOpen, 10)
	if len(open) != 0 {
		t.Fatalf("отклонённое предложено снова: %d черновиков", len(open))
	}
}

// Мера пользы: скилл, участвовавший в работе, получает исход работы.
func TestSkillScoreFollowsOutcome(t *testing.T) {
	_, st := newTestEngine(t, "", nil)
	if err := st.NoteSkillUse("linux-admin", 7); err != nil {
		t.Fatal(err)
	}
	if err := st.NoteSkillUse("mikrotik", 7); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSkillUses(7, "win"); err != nil {
		t.Fatal(err)
	}
	scores, err := st.SkillScores("2000-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 {
		t.Fatalf("скиллов в мере пользы %d", len(scores))
	}
	for _, sc := range scores {
		if sc.Wins != 1 || sc.Fails != 0 {
			t.Errorf("скилл %s: удач %d, срывов %d", sc.Name, sc.Wins, sc.Fails)
		}
	}
}

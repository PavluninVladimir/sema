package engine

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// createAccounts — MailConf ищет ящик среди заведённых, и для незнакомого
// имени молча отдаёт настройки безымянного. В тесте ящики надо завести.
func createAccounts(t *testing.T, st *store.Store, names ...string) {
	t.Helper()
	var list []*store.Account
	for _, i := range names {
		list = append(list, &store.Account{Name: i, Email: i, Host: "imap.проба", Port: 993})
	}
	if err := st.SaveAccounts(list); err != nil {
		t.Fatal(err)
	}
}

// У каждого ящика свои сроки удаления. Общая настройка — умолчание, а не
// закон: у рабочего ящика переписку не стирают вовсе, у личного спам
// живёт две недели, и одним числом на оба это не описать.
//
// Поле junk_days у ящика было с самого начала, и всё это время его никто
// не читал: чистка смотрела только в общую настройку. Человек задавал
// срок ящику, а работал общий — молча.
func TestOfAccountOwnDue(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting("junk.retention_days", "30"); err != nil {
		t.Fatal(err)
	}
	createAccounts(t, st, "личный@почта", "рабочий@почта")
	if err := e.SaveMailConf("личный@почта", &store.MailConf{JunkDays: 15}); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveMailConf("рабочий@почта", &store.MailConf{}); err != nil {
		t.Fatal(err)
	}
	if got := e.SpamTTLForAccount("личный@почта"); got != 15 {
		t.Errorf("свой срок ящика не читается: %d вместо 15", got)
	}
	if got := e.SpamTTLForAccount("рабочий@почта"); got != 30 {
		t.Errorf("ящик без своего срока должен идти за общим: %d вместо 30", got)
	}
}

// «Не решено» — это не «нет». Ящик, которому ничего не назначали, идёт
// за общей настройкой; ящик, которому сказали «нет», не стирает ничего,
// даже когда общая включена.
func TestNotDecidedIsNotNo(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	createAccounts(t, st, "молчун@почта", "рабочий@почта", "личный@почта")
	if err := e.SetPurgeRest(true); err != nil {
		t.Fatal(err)
	}
	no := false
	yes := true
	if err := e.SaveMailConf("молчун@почта", &store.MailConf{}); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveMailConf("рабочий@почта", &store.MailConf{PurgeOther: &no}); err != nil {
		t.Fatal(err)
	}
	if !e.PurgeRestForAccount("молчун@почта") {
		t.Error("ящик без решения не пошёл за общей настройкой")
	}
	if e.PurgeRestForAccount("рабочий@почта") {
		t.Error("ящик сказал «нет», а Сёма всё равно стирает его переписку")
	}
	// и наоборот: общая выключена, а этому ящику разрешили
	if err := e.SetPurgeRest(false); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveMailConf("личный@почта", &store.MailConf{PurgeOther: &yes}); err != nil {
		t.Fatal(err)
	}
	if !e.PurgeRestForAccount("личный@почта") {
		t.Error("разрешение ящика не сильнее общего запрета")
	}
	if e.PurgeRestForAccount("молчун@почта") {
		t.Error("молчащий ящик стирает переписку при выключенной общей настройке")
	}
}

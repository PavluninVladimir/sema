package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/ca"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Связь с агентами: пропуск — сертификат Сёмы, приглашение одноразовое,
// отзыв действует сразу, смена отпечатка — тревога.

func TestInviteBurnsOnce(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	code, err := e.NewInvite("мак", RoleResident)
	if err != nil {
		t.Fatalf("приглашение: %v", err)
	}
	_, pub, err := ca.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, rootPEM, role, err := e.Enroll(code, pub, []string{"код"})
	if err != nil {
		t.Fatalf("вход по приглашению: %v", err)
	}
	if ca.NameOf(certPEM) != "мак" || rootPEM == "" {
		t.Fatalf("выдан не тот сертификат: %q", ca.NameOf(certPEM))
	}
	if role != RoleResident {
		t.Fatalf("роль в ответе не та, что в приглашении: %q", role)
	}
	// второй раз тот же код не работает
	_, pub2, _ := ca.NewKey()
	if _, _, _, err := e.Enroll(code, pub2, nil); err == nil {
		t.Fatal("одноразовый код сработал дважды")
	}
	if _, _, _, err := e.Enroll("чужой-код", pub2, nil); err == nil {
		t.Fatal("выдуманный код сработал")
	}
}

func TestCallerCheckedByFingerprint(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	code, _ := e.NewInvite("мак", RoleResident)
	_, pub, _ := ca.NewKey()
	certPEM, _, _, err := e.Enroll(code, pub, []string{"код"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.checkCaller(certPEM)
	if err != nil || a.Name != "мак" {
		t.Fatalf("свой агент не опознан: %v", err)
	}
	// чужой центр не проходит
	foreign, _ := ca.NewRoot("Чужой", 1)
	_, pub2, _ := ca.NewKey()
	alien, _ := ca.Sign(foreign, "мак", pub2, 30)
	if _, err := e.checkCaller(alien); err == nil {
		t.Fatal("чужой сертификат принят")
	}
	// и это тревога: имя знакомое, отпечаток другой
	got, _ := st.Agent("мак")
	if got.State != store.AgentFPBad || got.NewFP == "" {
		t.Errorf("смена отпечатка не поднята тревогой: %+v", got.State)
	}
	// человек принял новый отпечаток
	if err := e.AcceptAgentFP("мак"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.checkCaller(alien); err != nil {
		t.Errorf("после принятия отпечатка агент не проходит: %v", err)
	}
}

func TestRevokeIsImmediate(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	code, _ := e.NewInvite("рабочая", RoleResident)
	_, pub, _ := ca.NewKey()
	certPEM, _, _, _ := e.Enroll(code, pub, []string{"код"})
	if err := e.AllowAgent("рабочая", "код", true); err != nil {
		t.Fatal(err)
	}
	if !e.AgentAllowed("рабочая", "код") {
		t.Fatal("разрешённая способность не разрешена")
	}
	if err := e.RevokeAgent("рабочая"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.checkCaller(certPEM); err == nil {
		t.Fatal("отозванный агент прошёл")
	}
	if e.AgentAllowed("рабочая", "код") {
		t.Error("после отзыва способность осталась разрешённой")
	}
}

// Способность, которую человек не разрешил, не вызывается.
func TestCapabilityNeedsPermission(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	code, _ := e.NewInvite("мак", RoleResident)
	_, pub, _ := ca.NewKey()
	if _, _, _, err := e.Enroll(code, pub, []string{"код", "команды"}); err != nil {
		t.Fatal(err)
	}
	if e.AgentAllowed("мак", "код") {
		t.Error("способность разрешена сама собой")
	}
	if err := e.AllowAgent("мак", "код", true); err != nil {
		t.Fatal(err)
	}
	if !e.AgentAllowed("мак", "код") {
		t.Error("разрешение не подействовало")
	}
	if e.AgentAllowed("мак", "команды") {
		t.Error("разрешение одной способности открыло другую")
	}
}

// Корневой ключ — секрет: он не должен показываться нигде в описании экрана.
func TestRootKeyStaysInside(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	root, err := e.EnsureRoot()
	if err != nil {
		t.Fatal(err)
	}
	if e.RootFingerprint() == "" {
		t.Error("отпечаток корня не виден человеку")
	}
	sec, ok := st.Secret(rootKeySecret)
	if !ok || sec.Kind != store.SecretSSH {
		t.Fatalf("закрытый ключ корня хранится не как ключ: %+v", sec)
	}
	if strings.Contains(e.RootFingerprint(), root.KeyPEM) {
		t.Error("закрытый ключ утёк в отпечаток")
	}
}

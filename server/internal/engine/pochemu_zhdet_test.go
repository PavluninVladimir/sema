package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Сейчас не окно самостоятельной работы» человек читал как поломку и не
// понимал, ждать ему или нажимать (13 сентября 2026). Запись обязана
// сказать, из-за чего команда стоит и что с этим делать.
func TestWhyWaitsSaysWhatDo(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveHost(&store.Host{Name: "wg", Addr: "1.2.3.4", User: "sema",
		Autonomy: store.AutonomyCourt, Window: "02:00-04:00"}); err != nil {
		t.Fatal(err)
	}
	p := &store.SSHPending{Host: "wg", Cmd: "sudo systemctl reload ssh", Who: "чат «Уведомление»"}
	if _, err := st.AddSSHPending(p); err != nil {
		t.Fatal(err)
	}
	line := whyWaits(e, p)
	if line == "" {
		t.Fatal("не сказано, почему команда ждёт")
	}
	if !strings.Contains(line, "Выполнить") {
		t.Errorf("не сказано, что делать: %q", line)
	}
	if !strings.Contains(line, "02:00-04:00") {
		t.Errorf("не названо окно сервера: %q", line)
	}
	// ручной сервер: причина другая и тоже понятная
	if err := st.SaveHost(&store.Host{Name: "ручной", Addr: "5.6.7.8", User: "sema"}); err != nil {
		t.Fatal(err)
	}
	manual := whyWaits(e, &store.SSHPending{Host: "ручной", Cmd: "sudo reboot"})
	if !strings.Contains(manual, "ручном") {
		t.Errorf("про ручной сервер сказано невнятно: %q", manual)
	}
	// остановленный сервер называет причину остановки
	if err := st.SaveHost(&store.Host{Name: "стоп", Addr: "9.9.9.9", User: "sema",
		Autonomy: store.AutonomyCourt, Paused: "проба не сошлась"}); err != nil {
		t.Fatal(err)
	}
	stop := whyWaits(e, &store.SSHPending{Host: "стоп", Cmd: "sudo reboot"})
	if !strings.Contains(stop, "проба не сошлась") || !strings.Contains(stop, "Продолжить") {
		t.Errorf("про остановленный сервер сказано невнятно: %q", stop)
	}
}

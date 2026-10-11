package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Окно самостоятельной работы 02:00–04:00, а задача, которая присылает
// команды, стоит на 8:00. Эти две настройки вместе значат «самостоятельной
// работы не будет никогда» — и 10 сентября 2026 нигде об этом не было
// сказано ни слова.
func TestWindowAndTimeCommandsNotMatch(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveHost(&store.Host{Name: "srv-bots", Addr: "1.2.3.4", User: "sema",
		Kind: "linux", Auto: true, Window: "02:00-04:00"}); err != nil {
		t.Fatal(err)
	}
	morning := time.Date(2026, 9, 10, 8, 2, 0, 0, time.Local)
	for i := 0; i < 3; i++ {
		if _, err := st.AddSSHPending(&store.SSHPending{Host: "srv-bots",
			Cmd: "проверка", Who: "задача «Утренняя проверка серверов»",
			Created: morning.Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
	}
	howMany, hours := e.WaitingOutsideWindow("srv-bots")
	if howMany != 3 {
		t.Errorf("вне окна насчитано %d команд из трёх", howMany)
	}
	if len(hours) != 1 || hours[0] != 8 {
		t.Errorf("час прихода команд: %v", hours)
	}
	line := e.WindowMismatch("srv-bots")
	for _, must := range []string{"02:00-04:00", "08:00", "ждать вас"} {
		if !strings.Contains(line, must) {
			t.Errorf("в объяснении нет %q: %q", must, line)
		}
	}
}

// Команда, пришедшая В окно, ждёт человека по другой причине — и валить
// это на окно нельзя: человек пойдёт менять настройку, которая ни при чём.
func TestCommandInWindowNotChargedOnWindow(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveHost(&store.Host{Name: "ночной", Addr: "1.2.3.4", User: "sema",
		Kind: "linux", Auto: true, Window: "02:00-04:00"}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 9, 10, 3, 0, 0, 0, time.Local)
	if _, err := st.AddSSHPending(&store.SSHPending{Host: "ночной", Cmd: "проверка",
		Who: "задача", Created: night.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if howMany, _ := e.WaitingOutsideWindow("ночной"); howMany != 0 {
		t.Errorf("команда в окне списана на окно: %d", howMany)
	}
	if s := e.WindowMismatch("ночной"); s != "" {
		t.Errorf("выдумано расхождение: %q", s)
	}
}

// У сервера без окна и без самостоятельности расхождению взяться неоткуда.
func TestWithoutWindowMismatchNo(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveHost(&store.Host{Name: "ручной", Addr: "1.2.3.4", User: "sema",
		Kind: "linux", Auto: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSSHPending(&store.SSHPending{Host: "ручной", Cmd: "проверка",
		Who: "человек", Created: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if s := e.WindowMismatch("ручной"); s != "" {
		t.Errorf("у ручного сервера выдумано расхождение: %q", s)
	}
}

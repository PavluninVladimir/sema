package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Настройка «включено» без включённой задачи — это отсутствие работы при
// надписи «работает».
//
// 11 сентября 2026 у человека не было ни одной резервной копии, хотя он
// включил «Копию наружу»: настройка встала в «1», а задача сердцебиения
// осталась выключенной. Тумблер щёлкает обе стороны с 10 сентября, но
// те, что разошлись ДО этого, так и остались врозь — и узнать правду
// было неоткуда.
func TestDivergedTogglesConsolidated(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	seedTask(t, e)
	for _, p := range TogglePairs {
		// человек включил настройку, а задача осталась выключенной
		if err := st.SetSetting(p.Setting, "1"); err != nil {
			t.Fatal(err)
		}
		if err := e.enableTask(p.Task, false); err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		e.VerifyToggles()
		if !e.taskEnabled(p.Task) {
			t.Errorf("«%s»: настройка говорит «включено», а задача так и стоит — работы нет", p.Name)
		}
	}
}

// И в другую сторону: выключил — значит выключено, а не «задача всё ещё
// ходит, просто проход молча ничего не делает».
func TestDisabledNotStaysEnabled(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	seedTask(t, e)
	for _, p := range TogglePairs {
		if err := st.SetSetting(p.Setting, "0"); err != nil {
			t.Fatal(err)
		}
		if err := e.enableTask(p.Task, true); err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		e.VerifyToggles()
		if e.taskEnabled(p.Task) {
			t.Errorf("«%s»: настройка выключена, а задача ходит", p.Name)
		}
	}
}

// Новая пара «настройка ↔ задача» не может появиться в обход сверки.
//
// Это главный тест из трёх: он ловит не нынешнюю беду, а следующую.
// Кто-то заведёт третий тумблер, щёлкающий задачу, забудет добавить его
// в список — и через месяц окажется, что чего-то не делалось.
func TestEachPairTaskAndSettingsVerified(t *testing.T) {
	root := "."
	has := map[string]bool{}
	for _, p := range TogglePairs {
		has[p.Task] = true
	}
	// имя константы в коде → её значение. Список нарочно руками: новая
	// пара, которой здесь нет, роняет тест — и это единственный способ
	// не дать ей появиться молча
	value := map[string]string{
		"WorkerBackup":       WorkerBackup,
		"WorkerFilesOffload": WorkerFilesOffload,
		"WorkerFilesMirror":  WorkerFilesMirror,
	}
	call := regexp.MustCompile(`enableTask\(([A-Za-zА-Яа-я_.]+)`)
	files, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range call.FindAllStringSubmatch(string(b), -1) {
			name := m[1]
			// сама сверка и её помощник зовут задачу по переменной цикла
			if name == "p.Task" || name == "system" {
				continue
			}
			val, known := value[name]
			if !known {
				t.Errorf("%s: задача %s включается настройкой, но тест о ней не знает — "+
					"добавьте её и в TogglePairs, и в этот список", filepath.Base(f), name)
				continue
			}
			if !has[val] {
				t.Errorf("%s: задача %s включается настройкой, но её нет в TogglePairs — "+
					"значит разойтись они смогут, а свести их будет некому", filepath.Base(f), name)
			}
		}
	}
}

// seedTask — встроенные задачи сердцебиения в тестовой Сёме: без
// них включать нечего, и сверка честно об этом говорит.
func seedTask(t *testing.T, e *Engine) {
	t.Helper()
	for i := range builtinTasks {
		z := builtinTasks[i]
		if err := e.store.EnsureSystemTask(&z); err != nil {
			t.Fatalf("встроенная задача %q: %v", z.Title, err)
		}
	}
}

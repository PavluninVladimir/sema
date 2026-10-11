package engine

import (
	"strings"
	"testing"
)

// Памяти не нужен ход мысли вслух — ей нужен короткий разбор. Локальная
// модель на Pi тратила весь пятиминутный предел роли на «думание» и
// обрывалась по сроку: за двое суток так пропали 62 сведения из 181.
func TestMemoryAsksNotThinkAloud(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting("роль.Память.модель", "нет"); err != nil {
		t.Fatal(err)
	}
	// модели нет — до неё дело не дойдёт, но промпт уже собран дверью
	_, _, err := e.askMemory(t.Context(), "сведи сессию")
	if err == nil {
		t.Fatal("без модели роль должна отказать")
	}
	if !strings.Contains(withoutThinking, "/no_think") {
		t.Error("выключатель размышлений потерялся")
	}
	if !strings.Contains(withoutThinking, "Do not reason out loud") {
		t.Error("моделям без /no_think нужна словами сказанная просьба")
	}
}

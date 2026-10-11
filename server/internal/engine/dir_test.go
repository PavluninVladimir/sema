package engine

import "testing"

func TestDebugCatalog(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	t.Logf("Files.Dir=%q Primary=%q", e.conf.Files.Dir, st.PrimaryStorage())
}

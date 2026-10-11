package store

import (
	"path/filepath"
	"testing"
)

// Признак «закрыт чисто» пишется последней транзакцией перед закрытием
// базы. Если поставить его раньше, он соврёт при падении на выходе — а по
// нему следующий запуск решает, проверять ли незавершённое.
func TestCleanShutdownFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "проба.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// первый запуск: пугать пустую установку нечем
	if !st.CleanShutdown() {
		t.Fatal("первый запуск посчитан грязным")
	}
	st.MarkRunning()
	if st.CleanShutdown() {
		t.Fatal("во время работы признак остался «чисто» — падение не заметим")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// открылись снова, как после убийства процесса
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if st2.CleanShutdown() {
		t.Fatal("после убийства признак «чисто» — так падение и пропустим")
	}
	st2.MarkRunning()
	st2.MarkCleanShutdown()
	if !st2.CleanShutdown() {
		t.Fatal("после честного закрытия признак не встал")
	}
}

package engine

import (
	"errors"
	"strings"
	"testing"
)

// Молчаливая поломка векторов стоила Сёме месяцев: адрес эмбеддера из
// настройки не применялся при старте, каждое письмо давало строку
// «lookup embed: no such host» в журнал — и всё. Поиск по смыслу не
// работал, память складывалась вслепую, а «Здоровье» было зелёным.
func TestFailuresVectorsCounted(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if inARow, _ := e.VectorsNotBuilt(); inARow != 0 {
		t.Fatalf("на чистой Сёме уже %d неудач", inARow)
	}
	trouble := errors.New(`Post "http://embed:8080/v1/embeddings": dial tcp: lookup embed: no such host`)
	for i := 0; i < 3; i++ {
		e.markVector(trouble)
	}
	inARow, why := e.VectorsNotBuilt()
	if inARow != 3 {
		t.Errorf("насчитано %d неудач из трёх", inARow)
	}
	if !strings.Contains(why, "no such host") {
		t.Errorf("причина потеряна: %q", why)
	}
	// первый успех снимает тревогу: чинить дважды не надо
	e.markVector(nil)
	if inARow, why := e.VectorsNotBuilt(); inARow != 0 || why != "" {
		t.Errorf("после успеха осталось %d неудач и причина %q", inARow, why)
	}
}

// В карточку идёт хвост ошибки, а не весь адрес с портом и путём.
func TestErrVectorsReadable(t *testing.T) {
	long := `Post "http://192.168.88.11:1234/v1/embeddings": dial tcp 192.168.88.11:1234: connect: connection refused`
	if got := briefErr(long); got != "connection refused" {
		t.Errorf("человеку показывают %q", got)
	}
	if got := briefErr(""); got == "" {
		t.Error("пустая причина должна называться словами")
	}
}

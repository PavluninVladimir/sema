package engine

import (
	"context"
	"sort"
	"strconv"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Каждый вид работы очереди обязан объявить свой след.
//
// Это тест на построение, а не на намерение. Сторож, который знает про виды
// работы снаружи, отстанет от кода на первом же новом виде — этим проект уже
// обжигался: «единственная дверь» mayGoOutside год стояла в коде, её не звал
// ни один рабочий путь, а тест засчитывал за проверку комментарий рядом.
//
// Список берётся из самой карты обработчиков, а не выписывается сюда руками:
// выписанный разошёлся бы с кодом ровно так же, как разошлись посев суточных
// расписаний и их разбор.
func TestOfEachKindWorkHasTrace(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	kinds := make([]string, 0, 8)
	for kind := range e.queueHandlers() {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	traces := e.traces()
	for _, kind := range kinds {
		if _, has := traces[kind]; !has {
			t.Errorf("вид работы %q не объявил следа: наблюдатель не увидит его петель. "+
				"Добавьте след в следы() или объявите следаНет явно", kind)
		}
	}
	// и наоборот: след без вида работы — забытая уборка
	for kind := range traces {
		if _, has := e.queueHandlers()[kind]; !has {
			t.Errorf("след объявлен для %q, а такого вида работы больше нет", kind)
		}
	}
}

// След обязан меняться, когда объект изменился, и совпадать, когда нет.
// Без этого он не отличает сделанную работу от потраченной.
func TestTraceChangesTogetherWithObject(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	a, _, err := e.AddFile(context.Background(), "счёт.txt", "text/plain",
		[]byte("Итого 1500"), FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := strconv.FormatInt(a.ID, 10)
	was, ok := e.Trace(WorkFileParse, ref)
	if !ok {
		t.Fatal("следа у файла нет")
	}
	res, _ := e.Trace(WorkFileParse, ref)
	if was != res {
		t.Error("след меняется сам по себе — сравнивать им нечего")
	}
	// разбор записал вид, продавца и сумму
	if err := st.MarkAttachment(a.ID, "receipt", "Лавка", 1500, "RUB", "чек из лавки"); err != nil {
		t.Fatal(err)
	}
	became, ok := e.Trace(WorkFileParse, ref)
	if !ok {
		t.Fatal("следа не стало")
	}
	if was == became {
		t.Error("объект изменился, а след прежний — петля будет невидима")
	}
}

// Объекта нет — нет и следа. Это не то же самое, что «ничего не
// изменилось»: удалённый объект не должен выглядеть зациклившимся.
func TestMissingObjectWithoutTrace(t *testing.T) {
	e, _ := newTestEngine(t, "тест", nil)
	if _, ok := e.Trace(WorkFileParse, "999999"); ok {
		t.Error("у несуществующего файла нашёлся след")
	}
	if _, ok := e.Trace("такого-вида-нет", "1"); ok {
		t.Error("у неизвестного вида работы нашёлся след")
	}
}

// следаНет — осознанный отказ, и он должен быть виден как отказ.
func TestExplicitDenialFromTrace(t *testing.T) {
	if _, ok := noTrace("что угодно"); ok {
		t.Error("явный отказ от следа вернул след")
	}
}

package engine

// Проверки распознавания на картинках из testdata/codes.
//
// Картинки там СГЕНЕРИРОВАНЫ (см. gen.go рядом с ними), а не сняты
// камерой: они проверяют, что читатель подключён, что порядок попыток
// работает и что разбор форматов верен. Мятый чек, блик и перспектива
// проверяются только на живом.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func pattern(t *testing.T, name string) (string, []byte) {
	t.Helper()
	path := filepath.Join("testdata", "codes", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("образец %s: %v", name, err)
	}
	return path, data
}

// TestCodesOnImages — по одному коду с каждой картинки, с замером
// времени: на Raspberry Pi процессор втрое слабее, и замер здесь —
// единственное, что о нём вообще можно сказать до выката.
func TestCodesOnImages(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	ctx := context.Background()

	cases := []struct {
		file      string
		format    string
		symbology string
		field     string
		value     string
	}{
		{"чек-фнс.png", store.CodeFNS, "qr", "Сумма", "55830.00"},
		{"чек-фнс.png", store.CodeFNS, "qr", "ФД", "4719"},
		{"чек-фнс-под-углом.png", store.CodeFNS, "qr", "ФД", "4719"},
		{"чек-фото.png", store.CodeFNS, "qr", "ФН", "7380440902615257"},
		{"ссылка-с-секретом.png", store.CodeSecretURL, "qr", "Куда ведёт", "pay.example.com"},
		{"марка-вода.png", store.CodeMark, "datamatrix", "GTIN", "04600439931256"},
		{"марка-вода.png", store.CodeMark, "datamatrix", "Серийный номер", "ABCDEFGHIJKLM"},
		{"марка-одежда.png", store.CodeMark, "datamatrix", "GTIN", "04601234567890"},
		{"марка-табак.png", store.CodeMark, "datamatrix", "GTIN", "04607428561777"},
		{"товар-ean13.png", store.CodeGTIN, "ean13", "GTIN", "4600682000150"},
		{"товар-весовой.png", store.CodeGTIN, "ean13", "Признак", "внутренний код магазина (весовой товар)"},
		{"товар-боком.png", store.CodeGTIN, "ean13", "GTIN", "4600682000150"},
	}
	for _, s := range cases {
		t.Run(s.file+"/"+s.field, func(t *testing.T) {
			path, data := pattern(t, s.file)
			start := time.Now()
			codes := e.ImageCodes(ctx, path, data, 0)
			t.Logf("%s: кодов %d за %v", s.file, len(codes), time.Since(start).Round(time.Millisecond))
			if len(codes) == 0 {
				t.Fatalf("%s: код не прочитан", s.file)
			}
			k := codes[0]
			if k.Format != s.format {
				t.Fatalf("%s: формат %q, ждали %q (поля %+v)", s.file, k.Format, s.format, k.Fields)
			}
			if k.Symbology != s.symbology {
				t.Errorf("%s: символика %q, ждали %q", s.file, k.Symbology, s.symbology)
			}
			if got := k.Field(s.field); got != s.value {
				t.Errorf("%s: поле %q = %q, ждали %q", s.file, s.field, got, s.value)
			}
		})
	}
}

// TestBrandTobaccoWithoutSeparators — известная граница разбора: настоящая
// табачная марка идёт без FNC1 и с четырёхзначным идентификатором цены
// (8005), которого Сёма не знает. GTIN при этом верный, а серийный номер
// приезжает вместе с хвостом. Тест закрепляет то, что есть на самом деле,
// чтобы это не выяснилось на живом товаре.
func TestBrandTobaccoWithoutSeparators(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	path, data := pattern(t, "марка-табак.png")
	codes := e.ImageCodes(context.Background(), path, data, 0)
	if len(codes) == 0 {
		t.Fatal("марка не прочитана")
	}
	if got := codes[0].Field("GTIN"); got != "04607428561777" {
		t.Fatalf("GTIN %q", got)
	}
	series := codes[0].Field("Серийный номер")
	if !strings.HasPrefix(series, "5Bfpwx") {
		t.Fatalf("серийный номер %q", series)
	}
	if series == "5Bfpwx" {
		t.Log("серийный номер отделился точно — разбор четырёхзначных идентификаторов появился, тест можно ужесточить")
	} else {
		t.Logf("известная граница: серийный номер %q приехал с хвостом цены (8005)", series)
	}
}

// TestImageWithoutCodes — честный пустой ответ и, главное, быстрый: файл
// без кодов не должен съедать весь предел времени.
func TestImageWithoutCodes(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	path, data := pattern(t, "без-кодов.png")
	start := time.Now()
	codes := e.ImageCodes(context.Background(), path, data, 0)
	passed := time.Since(start)
	t.Logf("без кодов: %v", passed.Round(time.Millisecond))
	if len(codes) != 0 {
		t.Fatalf("на пустой картинке нашлись коды: %+v", codes)
	}
	if passed > e.CodesLimit()+2*time.Second {
		t.Fatalf("искали %v при пределе %v", passed, e.CodesLimit())
	}

	// худший случай: большая фотография, на которой кода нет, — все
	// попытки и все читатели отрабатывают впустую. Это и есть то число,
	// которое на Raspberry Pi надо умножить примерно на три
	if err := e.EnableCodeKind("qr", false); err != nil {
		t.Fatal(err)
	}
	path, data = pattern(t, "чек-фото.png")
	start = time.Now()
	codes = e.ImageCodes(context.Background(), path, data, 0)
	t.Logf("худший случай (2400×3200 без кодов): %v", time.Since(start).Round(time.Millisecond))
	if len(codes) != 0 {
		t.Fatalf("QR выключен, а коды нашлись: %+v", codes)
	}
}

// TestSearchedAndNotFoundVisible — файл принят без кодов, но след остался:
// пустое место в карточке иначе не отличить от «даже не смотрели».
func TestSearchedAndNotFoundVisible(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	_, data := pattern(t, "без-кодов.png")
	att, _, err := e.AddFile(context.Background(), "лист.png", "image/png", data,
		FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatalf("приём файла: %v", err)
	}
	all := st.FileCodes(att.ID)
	no, ok := store.NotFound(all)
	if !ok {
		t.Fatalf("отметки «искали и не нашли» нет: %+v", all)
	}
	if no.Field("Искали") == "" {
		t.Error("в отметке не сказано, сколько искали")
	}
	if codes := e.FileCodes(att.ID); len(codes) != 0 {
		t.Fatalf("отметка просочилась в коды: %+v", codes)
	}
	if line := CodesForModel(all); line != "" {
		t.Fatalf("отметка уехала бы модели: %q", line)
	}
}

// TestLimitCodesHeld — вышло время, и дальше не ищем.
func TestLimitCodesHeld(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	path, data := pattern(t, "чек-фнс.png")
	if codes := e.imageCodesUntil(context.Background(), path, data, 0, time.Now().Add(-time.Second)); len(codes) != 0 {
		t.Fatalf("срок вышел, а коды всё равно искали: %+v", codes)
	}
}

// TestDisabledKindNotFound — виды кодов это настройка, а не гвоздь.
func TestDisabledKindNotFound(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.EnableCodeKind("qr", false); err != nil {
		t.Fatal(err)
	}
	path, data := pattern(t, "чек-фнс.png")
	if codes := e.ImageCodes(context.Background(), path, data, 0); len(codes) != 0 {
		t.Fatalf("QR выключен, а код прочитан: %+v", codes)
	}
	if err := e.EnableCodeKind("qr", true); err != nil {
		t.Fatal(err)
	}
	if codes := e.ImageCodes(context.Background(), path, data, 0); len(codes) == 0 {
		t.Fatal("QR включили обратно, а код не прочитан")
	}
}

// TestCodeNotRaisesClass — в QR чека лежат ФН и ФП, в марке — товар;
// ни то ни другое не личное. Если бы коды попадали в расчёт класса, слово
// «Сумма» из разбора закрыло бы файл от модели.
func TestCodeNotRaisesClass(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	path, data := pattern(t, "чек-фнс.png")
	att, _, err := e.AddFile(context.Background(), "чек.png", "image/png", data,
		FileSource{Kind: store.SourceUpload}, nil)
	if err != nil {
		t.Fatalf("приём файла: %v", err)
	}
	_ = path
	if class := e.ClassOfFile(att); class != store.ClassPlain {
		t.Fatalf("класс файла %q, ждали обычное", class)
	}
	codes := e.Store().FileCodes(att.ID)
	if len(codes) == 0 {
		t.Fatal("коды не сохранились при приёме файла")
	}
	if codes[0].Format != store.CodeFNS {
		t.Fatalf("формат %q", codes[0].Format)
	}
}

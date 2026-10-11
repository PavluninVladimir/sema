package engine

// Коды на страницах PDF без текстового слоя: чек часто приходит не
// фотографией, а сканом в PDF, и pdftotext на нём молчит.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestCodesOnPagesPDF(t *testing.T) {
	if !havePdftoppm {
		t.Skip("нет pdftoppm: страницы PDF нечем нарисовать")
	}
	e, _ := newTestEngine(t, "", nil)
	_, png := pattern(t, "чек-фнс.png")
	pdf := pdfWithImage(t, png)
	path := filepath.Join(t.TempDir(), "скан.pdf")
	if err := os.WriteFile(path, pdf, 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	text, via, codes := e.extract(context.Background(), path, "application/pdf", pdf)
	t.Logf("PDF-скан: кодов %d за %v (текст %d знаков, %s)", len(codes),
		time.Since(start).Round(time.Millisecond), len([]rune(text)), via)
	if len(codes) == 0 {
		t.Fatal("на странице PDF код не прочитан")
	}
	if codes[0].Format != store.CodeFNS {
		t.Fatalf("формат %q", codes[0].Format)
	}
	if codes[0].Page != 1 {
		t.Errorf("страница %d, ждали 1", codes[0].Page)
	}
	if codes[0].Field("ФД") != "4719" {
		t.Errorf("ФД %q", codes[0].Field("ФД"))
	}
}

// pdfWithImage — однастраничный PDF с картинкой на всю страницу и без
// единой буквы: ровно то, чем оборачивают скан чека. Собран руками,
// чтобы не тащить библиотеку ради одного теста.
func pdfWithImage(t *testing.T, raw []byte) []byte {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatal(err)
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()

	var b bytes.Buffer
	offset := []int{0}
	object := func(text string, stream []byte) {
		offset = append(offset, b.Len())
		b.WriteString(text)
		if stream != nil {
			b.WriteString("\nstream\n")
			b.Write(stream)
			b.WriteString("\nendstream")
		}
		b.WriteString("\nendobj\n")
	}
	b.WriteString("%PDF-1.4\n")
	object("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>", nil)
	object("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>", nil)
	object("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]"+
		" /Resources << /XObject << /Im0 4 0 R >> >> /Contents 5 0 R >>", nil)
	object(fmt.Sprintf("4 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>", w, h, jpg.Len()), jpg.Bytes())
	canvas := "q 500 0 0 500 56 200 cm /Im0 Do Q"
	object(fmt.Sprintf("5 0 obj\n<< /Length %d >>", len(canvas)), []byte(canvas))

	xref := b.Len()
	var t2 strings.Builder
	fmt.Fprintf(&t2, "xref\n0 %d\n0000000000 65535 f \n", len(offset))
	for _, o := range offset[1:] {
		fmt.Fprintf(&t2, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&t2, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offset), xref)
	b.WriteString(t2.String())
	return b.Bytes()
}

//go:build ignore

// Генератор образцов для тестов распознавания кодов.
//
// Запуск: go run ./internal/engine/testdata/codes/gen.go
//
// Это СГЕНЕРИРОВАННЫЕ картинки, а не съёмка. Они проверяют, что читатель
// подключён, что разбор форматов верен и что порядок попыток работает, —
// но не то, как Сёма справится с мятым чеком, бликом от лампы и
// перспективой. Это проверяется на живом (пункты 5.1 и 5.2 задачи).
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/datamatrix"
	"github.com/makiuchi-d/gozxing/oned"
	"github.com/makiuchi-d/gozxing/qrcode"
)

const gs = "\x1d" // FNC1 внутри GS1 DataMatrix

func main() {
	dir := filepath.Dir(os.Args[0])
	if len(os.Args) > 1 {
		dir = os.Args[1]
	} else if wd, err := os.Getwd(); err == nil {
		dir = filepath.Join(wd, "internal/engine/testdata/codes")
	}
	must(os.MkdirAll(dir, 0o755))

	qr := qrcode.NewQRCodeWriter()
	чек, err := qr.Encode("t=20260406T1847&s=55830.00&fn=7380440902615257&i=4719&fp=3641530653&n=1",
		gozxing.BarcodeFormat_QR_CODE, 360, 360, nil)
	must(err)
	save(dir, "чек-фнс.png", fields(чек, 30))
	save(dir, "чек-фнс-под-углом.png", rotate(fields(чек, 80), 12))
	save(dir, "чек-фото.png", placeOnSheet(чек, 2400, 3200))

	ссылка, err := qr.Encode("https://pay.example.com/inv/8842?token=s3cr3t-abcdef123456",
		gozxing.BarcodeFormat_QR_CODE, 300, 300, nil)
	must(err)
	save(dir, "ссылка-с-секретом.png", fields(ссылка, 30))

	dm := datamatrix.NewDataMatrixWriter()
	марки := map[string]string{
		// вода и одежда: серийный номер переменной длины, закрыт FNC1 —
		// так марка и выглядит по GS1
		"марка-вода.png":   "0104600439931256" + "21ABCDEFGHIJKLM" + gs + "93dGVz",
		"марка-одежда.png": "0104601234567890" + "2159qMuFCkjR1sT" + gs + "91EE06" + gs + "92RjBmqEerVtcgBRyPJFiuR0Pki",
		// табак: настоящая марка идёт без разделителей, серийный номер в
		// семь знаков, дальше 8005 (цена). Четырёхзначные идентификаторы
		// Сёма не разбирает — образец нужен, чтобы это было видно тестом,
		// а не выяснилось на живом товаре
		"марка-табак.png": "0104607428561777" + "215Bfpwx" + "8005112000" + gs + "93Zjqw",
	}
	for имя, значение := range марки {
		m, err := dm.Encode(значение, gozxing.BarcodeFormat_DATA_MATRIX, 320, 320, nil)
		must(err)
		save(dir, имя, fields(m, 30))
	}

	ean := oned.NewEAN13Writer()
	товар, err := ean.Encode("4600682000150", gozxing.BarcodeFormat_EAN_13, 380, 160, nil)
	must(err)
	save(dir, "товар-ean13.png", fields(товар, 30))
	весовой, err := ean.Encode("2123456789010", gozxing.BarcodeFormat_EAN_13, 380, 160, nil)
	must(err)
	save(dir, "товар-весовой.png", fields(весовой, 30))
	save(dir, "товар-боком.png", sideways(fields(товар, 40)))

	save(dir, "без-кодов.png", sheet(900, 1200))
	fmt.Println("готово:", dir)
}

// fields — белая рамка вокруг кода: без «тихой зоны» ни один читатель кода
// не видит, и это не особенность библиотеки, а требование стандарта.
func fields(m *gozxing.BitMatrix, отступ int) image.Image {
	b := m.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()+2*отступ, b.Dy()+2*отступ))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, image.Rect(отступ, отступ, отступ+b.Dx(), отступ+b.Dy()), m, b.Min, draw.Src)
	return dst
}

// placeOnSheet — код в углу большого листа: так выглядит фотография чека
// с телефона, где сам QR занимает малую часть кадра.
func placeOnSheet(m *gozxing.BitMatrix, w, h int) image.Image {
	dst := sheet(w, h).(*image.RGBA)
	b := m.Bounds()
	draw.Draw(dst, image.Rect(w-b.Dx()-200, 300, w-200, 300+b.Dy()), m, b.Min, draw.Src)
	return dst
}

// sheet — светлый фон с лёгкой неровностью: ровно белый лист бинаризатору
// слишком удобен, а настоящая бумага так не выглядит.
func sheet(w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 230 + (x*7+y*3)%20
			dst.Set(x, y, color.RGBA{R: uint8(v), G: uint8(v), B: uint8(v), A: 255})
		}
	}
	return dst
}

// sideways — поворот ровно на 90 градусов с перестановкой сторон: так
// выглядит штрих-код, снятый вертикально. Поворот в той же рамке
// (повернуть ниже) для этого не годится — код обрежется краями.
func sideways(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// rotate — поворот на произвольный угол вокруг центра, белым по краям.
// Так моделируется съёмка под углом: настоящую перспективу этим не
// заменить, но наклон камеры — да.
func rotate(src image.Image, градусы float64) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	угол := градусы * math.Pi / 180
	sin, cos := math.Sin(угол), math.Cos(угол)
	cx, cy := float64(w)/2, float64(h)/2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			sx := int(cx + dx*cos + dy*sin)
			sy := int(cy - dx*sin + dy*cos)
			if sx < 0 || sy < 0 || sx >= w || sy >= h {
				continue
			}
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

func save(dir, name string, img image.Image) {
	f, err := os.Create(filepath.Join(dir, name))
	must(err)
	defer f.Close()
	must(png.Encode(f, img))
	fmt.Println("  ", name)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

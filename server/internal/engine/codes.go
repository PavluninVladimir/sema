package engine

// Коды на картинках и страницах PDF: QR чека ФНС, марка «Честного знака»,
// штрих-код товара.
//
// Читает их сам сервер — библиотекой gozxing (порт ZXing на чистый Go, без
// cgo, иначе не собралось бы под Raspberry Pi). Модель тут не участвует
// нарочно: модели читают QR плохо и при этом уверенно называют цифры,
// которых на картинке нет, а весь смысл кода как раз в точности. Сеть тоже
// не нужна: код расшифровывается на месте.
//
// Порядок попыток — от дешёвой к дорогой: уменьшенная копия, затем
// исходник, затем повороты уменьшенной. Фотография с телефона — двенадцать
// мегапикселей, и на Pi разбор её в лоб стоит секунды, а на уменьшенной
// копии код виден тот же самый. Повороты нужны не QR (он сам находит свою
// ориентацию), а полосатым кодам: снятый вертикально EAN построчный
// читатель не видит вовсе.

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	_ "image/gif"  // картинки из писем приходят в чём угодно
	_ "image/jpeg" // фотографии чеков — почти всегда jpeg
	_ "image/png"  // страницы PDF Сёма рисует в png

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/aztec"
	"github.com/makiuchi-d/gozxing/datamatrix"
	"github.com/makiuchi-d/gozxing/oned"
	"github.com/makiuchi-d/gozxing/qrcode"
)

const (
	codesKindsKey = "codes.kinds" // какие символики искать
	codesMaxKey   = "codes.max_s" // предел времени на файл
	codesMpKey    = "codes.max_mp"

	// длинная сторона уменьшенной копии: на ней код с фотографии читается
	// так же, а работы в разы меньше
	codesMaxSide = 1600
)

// codeKind — символика и читатель для неё. Список читателей живёт в коде,
// потому что читатель — это код; включённость каждого — настройка.
// PDF417 в списке нет: gozxing его не умеет вовсе, и обещать в интерфейсе
// то, чего нет, хуже, чем не обещать.
type codeKind struct {
	Key    string
	Title  string
	reader func() gozxing.Reader
}

var codeKinds = []codeKind{
	{"qr", "QR-код", func() gozxing.Reader { return qrcode.NewQRCodeReader() }},
	{"datamatrix", "DataMatrix (марки «Честного знака»)", func() gozxing.Reader { return datamatrix.NewDataMatrixReader() }},
	{"aztec", "Aztec", func() gozxing.Reader { return aztec.NewAztecReader() }},
	{"ean", "Штрих-код товара (EAN-13/8, UPC)", func() gozxing.Reader { return oned.NewMultiFormatUPCEANReader(nil) }},
	{"code128", "Code 128", func() gozxing.Reader { return oned.NewCode128Reader() }},
}

// CodeKind — строка справочника для экрана «Инструменты».
type CodeKind struct {
	Key     string
	Title   string
	Enabled bool
}

// CodeKinds — что Сёма умеет искать и что из этого включено.
func (e *Engine) CodeKinds() []CodeKind {
	on := e.enabledKinds()
	out := make([]CodeKind, 0, len(codeKinds))
	for _, v := range codeKinds {
		out = append(out, CodeKind{Key: v.Key, Title: v.Title, Enabled: on[v.Key]})
	}
	return out
}

// EnableCodeKind — тумблер вида на «Инструментах».
func (e *Engine) EnableCodeKind(key string, enabled bool) error {
	on := e.enabledKinds()
	on[key] = enabled
	var list []string
	for _, v := range codeKinds {
		if on[v.Key] {
			list = append(list, v.Key)
		}
	}
	if len(list) == 0 {
		// пустая строка означала бы «по умолчанию, то есть все»: человек,
		// выключивший последний вид, получил бы обратно все
		return e.store.SetSetting(codesKindsKey, "-")
	}
	return e.store.SetSetting(codesKindsKey, strings.Join(list, ","))
}

func (e *Engine) enabledKinds() map[string]bool {
	on := map[string]bool{}
	saved := strings.TrimSpace(e.store.Setting(codesKindsKey, ""))
	if saved == "" {
		for _, v := range codeKinds {
			on[v.Key] = true
		}
		return on
	}
	for _, k := range strings.Split(saved, ",") {
		on[strings.TrimSpace(k)] = true
	}
	return on
}

// CodesLimit — сколько времени даётся на коды одного файла. Не уложились
// — файл живёт без кодов: приём других файлов ждать этого не должен.
func (e *Engine) CodesLimit() time.Duration {
	n := e.store.SettingInt(codesMaxDef)
	if n > codesMaxDef.Max {
		n = codesMaxDef.DefaultInt()
	}
	return time.Duration(n) * time.Second
}

func (e *Engine) SetCodesLimit(sec int) error {
	if sec <= 0 || sec > 600 {
		sec = 8
	}
	return e.store.SetSetting(codesMaxKey, strconv.Itoa(sec))
}

// PixelsLimit — сколько мегапикселей разрешено развернуть в памяти.
// Сжатая картинка бывает маленькой, а развёрнутая — гигабайтом: на Pi это
// не «медленно», а конец процессу.
func (e *Engine) PixelsLimit() int {
	n := e.store.SettingInt(codesMpDef)
	if n > codesMpDef.Max {
		n = codesMpDef.DefaultInt()
	}
	return n
}

func (e *Engine) SetPixelsLimit(mp int) error {
	if mp <= 0 || mp > 400 {
		mp = 40
	}
	return e.store.SetSetting(codesMpKey, strconv.Itoa(mp))
}

// ImageCodes — коды на одной картинке. Страница — номер страницы PDF;
// 0 значит «картинка целиком».
func (e *Engine) ImageCodes(ctx context.Context, path string, data []byte, page int) []store.FileCode {
	due := time.Now().Add(e.CodesLimit())
	return e.imageCodesUntil(ctx, path, data, page, due)
}

// imageCodesUntil — то же, но с общим сроком на весь файл: у PDF страниц
// несколько, и каждой давать полный предел значило бы умножить его на пять.
func (e *Engine) imageCodesUntil(ctx context.Context, path string, data []byte, page int, due time.Time) []store.FileCode {
	readers := e.enabledReaders()
	if len(readers) == 0 || !due.After(time.Now()) {
		return nil
	}
	if len(data) == 0 {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return nil
		}
	}
	img, ok := expand(data, e.PixelsLimit())
	if !ok {
		return nil
	}
	codes := findCodes(ctx, img, readers, due)
	out := make([]store.FileCode, 0, len(codes))
	for _, k := range codes {
		k.Page = page
		out = append(out, e.parseValue(k))
	}
	return out
}

// enabledReaders — читатели видов, которые человек оставил включёнными.
func (e *Engine) enabledReaders() []codeKind {
	on := e.enabledKinds()
	var out []codeKind
	for _, v := range codeKinds {
		if on[v.Key] {
			out = append(out, v)
		}
	}
	return out
}

// expand — картинка из байтов с проверкой размера до разворачивания.
func expand(data []byte, limitMP int) (image.Image, bool) {
	conf, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, false // формат, которого стандартная библиотека не знает: HEIC, WebP
	}
	if conf.Width <= 0 || conf.Height <= 0 || conf.Width*conf.Height > limitMP*1_000_000 {
		log.Printf("коды: картинка %dx%d больше предела %d Мп — пропускаем", conf.Width, conf.Height, limitMP)
		return nil, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	return img, true
}

// findCodes — сам порядок попыток. Прерваться посреди разбора кадра
// gozxing не умеет, поэтому срок проверяется между попытками: обещание
// «не дольше восьми секунд» честно с точностью до одного кадра.
func findCodes(ctx context.Context, img image.Image, readers []codeKind, due time.Time) []store.FileCode {
	small := shrink(img, codesMaxSide)
	attempts := []image.Image{small}
	if small != img {
		attempts = append(attempts, img) // на исходнике виден мелкий код, потерянный при уменьшении
	}
	attempts = append(attempts, rotate(small, 90), rotate(small, 180), rotate(small, 270))
	for _, frame := range attempts {
		if ctx.Err() != nil || time.Now().After(due) {
			return nil
		}
		if codes := readFrame(frame, readers, due); len(codes) > 0 {
			return codes
		}
	}
	return nil
}

// readFrame — все включённые читатели по одному кадру.
func readFrame(img image.Image, readers []codeKind, due time.Time) []store.FileCode {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil
	}
	// TRY_HARDER — это как раз про фотографию: без него читается только
	// ровно снятый код, а чек на столе снимают под углом
	hints := map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_TRY_HARDER: true}
	var out []store.FileCode
	seen := map[string]bool{}
	for _, v := range readers {
		if time.Now().After(due) {
			break
		}
		res, err := v.reader().Decode(bmp, hints)
		if err != nil || res == nil {
			continue
		}
		value := res.GetText()
		if strings.TrimSpace(value) == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, store.FileCode{Symbology: symbology(res.GetBarcodeFormat(), v.Key), Value: value})
	}
	return out
}

// symbology — как называется найденное. Имя формата берём у самой
// библиотеки: у «ean» читатель один, а форматов за ним четыре.
func symbology(f gozxing.BarcodeFormat, key string) string {
	switch f {
	case gozxing.BarcodeFormat_QR_CODE:
		return "qr"
	case gozxing.BarcodeFormat_DATA_MATRIX:
		return "datamatrix"
	case gozxing.BarcodeFormat_AZTEC:
		return "aztec"
	case gozxing.BarcodeFormat_EAN_13:
		return "ean13"
	case gozxing.BarcodeFormat_EAN_8:
		return "ean8"
	case gozxing.BarcodeFormat_UPC_A:
		return "upca"
	case gozxing.BarcodeFormat_UPC_E:
		return "upce"
	case gozxing.BarcodeFormat_CODE_128:
		return "code128"
	}
	return key
}

// shrink — копия картинки в серых тонах с длинной стороной не больше
// предела. Усреднение по клетке, а не выборка каждого N-го пикселя: при
// выборке тонкие полосы штрих-кода пропадают целиком.
func shrink(src image.Image, side int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= side && h <= side {
		return src
	}
	step := (w + side - 1) / side
	if s := (h + side - 1) / side; s > step {
		step = s
	}
	dst := image.NewGray(image.Rect(0, 0, (w+step-1)/step, (h+step-1)/step))
	for y := 0; y < dst.Rect.Dy(); y++ {
		for x := 0; x < dst.Rect.Dx(); x++ {
			var amount, count uint32
			for dy := 0; dy < step; dy++ {
				sy := b.Min.Y + y*step + dy
				if sy >= b.Max.Y {
					break
				}
				for dx := 0; dx < step; dx++ {
					sx := b.Min.X + x*step + dx
					if sx >= b.Max.X {
						break
					}
					r, g, bl, _ := src.At(sx, sy).RGBA()
					amount += (r*299 + g*587 + bl*114) / 1000 >> 8
					count++
				}
			}
			if count > 0 {
				avg := amount / count
				if avg > 255 {
					avg = 255
				}
				dst.SetGray(x, y, color.Gray{Y: uint8(avg)})
			}
		}
	}
	return dst
}

// rotate — поворот на 90, 180 или 270 градусов.
func rotate(src image.Image, degrees int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	var dst *image.RGBA
	switch degrees {
	case 90, 270:
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
	default:
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.At(b.Min.X+x, b.Min.Y+y)
			switch degrees {
			case 90:
				dst.Set(h-1-y, x, c)
			case 180:
				dst.Set(w-1-x, h-1-y, c)
			default:
				dst.Set(y, w-1-x, c)
			}
		}
	}
	return dst
}

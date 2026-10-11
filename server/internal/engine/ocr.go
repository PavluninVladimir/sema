package engine

// Собственное распознавание картинок. На маке берём системный Vision —
// он уже есть, ставить ничего не нужно и картинки никуда не уходят; на
// Raspberry Pi, VPS и в Docker работает tesseract. Что именно доступно,
// видно в настройках, вкладка «Система».

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
)

// ocrSwift — крошечный помощник на Swift: читает картинку системным
// Vision и печатает найденный текст. Собирается один раз при первом вызове.
const ocrSwift = `import Foundation
import Vision
import AppKit

guard CommandLine.arguments.count > 1,
      let image = NSImage(contentsOfFile: CommandLine.arguments[1]),
      let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil) else { exit(1) }

let request = VNRecognizeTextRequest()
request.recognitionLevel = .accurate
request.recognitionLanguages = ["ru-RU", "en-US"]
request.usesLanguageCorrection = true

do {
    try VNImageRequestHandler(cgImage: cg, options: [:]).perform([request])
} catch { exit(1) }

for case let line as VNRecognizedTextObservation in request.results ?? [] {
    if let best = line.topCandidates(1).first { print(best.string) }
}
`

var (
	visionOnce sync.Once
	visionPath string      // собранный помощник; пусто — недоступен
	visionDone atomic.Bool // проверка (и сборка) закончена: visionPath можно читать
)

// systemOCR — путь к системному распознавателю, собирая его при первом
// обращении. Пустая строка означает, что на этой машине его нет.
func systemOCR(dir string) string {
	visionOnce.Do(func() {
		defer visionDone.Store(true)
		if runtime.GOOS != "darwin" {
			return
		}
		if _, err := exec.LookPath("swiftc"); err != nil {
			return
		}
		// помощник живёт в кэше пользователя, а не в каталоге данных: он
		// про машину, а не про Сёму, и собирается один раз на машину. Имя
		// несёт отпечаток исходника — правка исходника пересобирает его.
		// Проверка идёт и в фоне (HaveOurOCRNow), и в каталог данных теста,
		// который уже удаляют, фоновая сборка писать не должна
		toolDir := filepath.Join(dir, "tools")
		if c, err := os.UserCacheDir(); err == nil {
			toolDir = filepath.Join(c, "sema-ocr")
		}
		if err := os.MkdirAll(toolDir, 0o755); err != nil {
			return
		}
		sum := sha256.Sum256([]byte(ocrSwift))
		bin := filepath.Join(toolDir, "semaocr-"+hex.EncodeToString(sum[:4]))
		if _, err := os.Stat(bin); err == nil {
			visionPath = bin
			return
		}
		// сборка во временном каталоге и переезд одним rename: два процесса
		// (параллельные пакеты тестов) не видят друг у друга полсобранного
		tmp, err := os.MkdirTemp(toolDir, "build-")
		if err != nil {
			return
		}
		defer os.RemoveAll(tmp)
		src := filepath.Join(tmp, "semaocr.swift")
		if err := os.WriteFile(src, []byte(ocrSwift), 0o644); err != nil {
			return
		}
		built := filepath.Join(tmp, "semaocr")
		if out, err := exec.Command("swiftc", "-O", src, "-o", built).CombinedOutput(); err != nil {
			log.Printf("сборка распознавателя картинок: %v: %s", err, out)
			return
		}
		if err := os.Rename(built, bin); err != nil {
			return
		}
		visionPath = bin
	})
	return visionPath
}

// ourOCR — распознавание картинки без внешних сервисов, тем движком,
// который выбран в настройках.
func (e *Engine) ourOCR(ctx context.Context, path string) string {
	switch e.OCREngine() {
	case OCRModelOnly:
		return "" // человек выбрал только модель
	case OCRTesseract, OCRModelThenTess:
		return ocrText(ctx, path)
	}
	if bin := systemOCR(e.conf.Files.Dir); bin != "" {
		if out, err := extractCommand(ctx, bin, path); err == nil {
			return string(out)
		}
	}
	return ocrText(ctx, path) // если Vision почему-то не собрался
}

// HaveOurOCRNow — то же, что HaveOurOCR, но без ожидания: экран не ждёт
// компилятора. На маке первая проверка собирает помощник swiftc — полсекунды
// и больше, — и «Обзор» настроек ждал её при первом открытии (perf-pass 4.1:
// браузерный обход 1,7 с). Не проверено — проверка уходит в фон, ответ
// known = false; Run запускает её сразу при старте.
func (e *Engine) HaveOurOCRNow() (have bool, how string, known bool) {
	if visionDone.Load() {
		have, how = e.HaveOurOCR()
		return have, how, true
	}
	// фоном проверяет только работающий движок (Run): тесты и утилиты без
	// него не оставляют после себя компилятора и полусобранного каталога
	if e.engineUp.Load() {
		go systemOCR(e.conf.Files.Dir)
	}
	if haveTesseract {
		return true, "tesseract", true
	}
	return false, "", false
}

// HaveOurOCR — есть ли на машине хоть какое-то своё распознавание.
func (e *Engine) HaveOurOCR() (bool, string) {
	if systemOCR(e.conf.Files.Dir) != "" {
		return true, "системный Vision · macOS, картинки не покидают компьютер"
	}
	if haveTesseract {
		return true, "tesseract"
	}
	return false, ""
}

// Выбор человека: чем читать картинки. Пусто — «как получится»:
// сначала модель, потом любое своё распознавание.
const (
	ocrEngineKey   = "ocr_engine" // vision | tesseract | none
	visionModelKey = "vision_model"
)

// Чем читаем картинки — один список: сама модель, наши распознаватели или
// связка «модель, затем наш OCR», когда модель видит картинку и получает
// наш текст подсказкой.
const (
	OCRModelThenVision = "Модель, затем Vision"
	OCRModelThenTess   = "Модель, затем tesseract"
	OCRModelOnly       = "Только модель"
	OCRVision          = "Только Vision (macOS)"
	OCRTesseract       = "Только tesseract"
)

// OCREngines — варианты, доступные на этой машине.
func (e *Engine) OCREngines() []string {
	hasVision := systemOCR(e.conf.Files.Dir) != ""
	var out []string
	if hasVision {
		out = append(out, OCRModelThenVision)
	}
	if haveTesseract {
		out = append(out, OCRModelThenTess)
	}
	out = append(out, OCRModelOnly)
	if hasVision {
		out = append(out, OCRVision)
	}
	if haveTesseract {
		out = append(out, OCRTesseract)
	}
	return out
}

// OCREngineSaved — что выбрал человек, как записано: без выяснения, какие
// распознаватели есть на машине (первое обращение к системному собирает
// помощника компилятором). Нужен индексу настроек, который ничего не собирает.
func (e *Engine) OCREngineSaved() string { return e.store.Setting(ocrEngineKey, "") }

func (e *Engine) OCREngine() string {
	list := e.OCREngines()
	saved := e.store.Setting(ocrEngineKey, list[0])
	for _, v := range list {
		if v == saved {
			return saved
		}
	}
	return list[0] // выбранный инструмент исчез с машины
}

// useModelForImages — показываем ли картинку модели при этом выборе.
func (e *Engine) useModelForImages() bool {
	switch e.OCREngine() {
	case OCRVision, OCRTesseract:
		return false
	}
	return true
}

func (e *Engine) SetOCREngine(v string) error { return e.store.SetSetting(ocrEngineKey, v) }

// VisionModel — модель роли «Чтение картинок».
func (e *Engine) VisionModel() string {
	if m := e.Role(RoleVision).Model; m != "" {
		return m
	}
	return e.store.Setting(visionModelKey, e.ActiveProvider())
}

func (e *Engine) SetVisionModel(v string) error {
	return e.SetRoleField(RoleVision, "model", v)
}

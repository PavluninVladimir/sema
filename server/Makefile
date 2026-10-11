# Сборка Сёмы под разные машины. Везде CGO_ENABLED=0 — один бинарник,
# никаких зависимостей в системе, кроме pdftotext для PDF-чеков.
BIN := sema-server
# версия сборки: метка git, иначе коммит; видна на «Здоровье» и в -doctor
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -trimpath -ldflags=-s\ -w\ -X\ github.com/PavluninVladimir/sema/server/internal/version.Version=$(VERSION)

.PHONY: build mac linux pi docker run clean icons

build:            ## для текущей машины
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BIN) ./cmd/sema

mac: build

linux:            ## VPS x86-64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BIN)-linux-amd64 ./cmd/sema

pi:               ## Raspberry Pi / ARM-VPS
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BIN)-linux-arm64 ./cmd/sema

docker:           ## образ под текущую архитектуру
	docker build --build-arg VERSION=$(VERSION) -t sema:latest .

sums:             ## суммы sha256 к сборкам: публикуются рядом с файлами
	(shasum -a 256 $(BIN) $(BIN)-linux-* 2>/dev/null || sha256sum $(BIN) $(BIN)-linux-*) > SHA256SUMS
	@echo "проверка у человека: shasum -a 256 -c SHA256SUMS; отпечаток образа: docker image inspect sema:latest --format '{{index .RepoDigests 0}}'"

run: build
	./$(BIN) -config config.yaml

icons:                  ## растровые иконки из SVG (требует ImageMagick на маке)
	cd internal/web/static && \
	magick icon.svg -resize 192x192 icon-192.png && \
	magick icon.svg -resize 512x512 icon-512.png && \
	magick icon-maskable.svg -resize 180x180 icon-180.png && \
	magick icon-maskable.svg -resize 512x512 icon-maskable-512.png && \
	magick -background none -density 384 icon-badge.svg -resize 96x96 PNG32:icon-badge.png

clean:
	rm -f $(BIN) $(BIN)-linux-*

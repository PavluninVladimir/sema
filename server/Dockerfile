# Сборка: чистый Go без cgo — тот же бинарник собирается под amd64 и arm64,
# поэтому Сёма одинаково едет и на VPS, и на Raspberry Pi.
FROM golang:1.26.8-alpine3.24 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# TARGETARCH подставляет сам сборщик. Значения по умолчанию тут быть не
# должно: оно побеждает, и образ с меткой arm64 уносит внутри бинарник
# x86-64 — на маке это скрывает эмуляция, а на Pi «exec format error».
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X github.com/PavluninVladimir/sema/server/internal/version.Version=${VERSION}" -o /out/sema ./cmd/sema

# Рантайм: poppler нужен для чтения PDF-чеков (pdftotext),
# ca-certificates — для OAuth и запросов из скриптов.
FROM alpine:3.24

# tesseract — распознавание картинок на Linux (на маке эту работу делает Vision)
# sqlite — чтобы смотреть базу через docker exec, а не с хозяина: снаружи
# блокировки не работают, и база уже один раз была повреждена так
RUN apk add --no-cache ca-certificates poppler-utils tzdata tesseract-ocr tesseract-ocr-data-rus tesseract-ocr-data-eng sqlite \
 && adduser -D -u 10001 sema

WORKDIR /data
COPY --from=build /out/sema /usr/local/bin/sema

# База, архив писем и вложения живут в томе — переживают обновление образа
VOLUME ["/data"]
EXPOSE 8765
USER sema

# Часовой пояс берётся из браузера (настройка app.timezone) или задаётся
# при запуске: -e TZ=Europe/Berlin. Модели на хозяине (LM Studio, Ollama)
# из контейнера видны по имени хозяина: -e SEMA_HOST_ALIAS=host.docker.internal
# подменяет «127.0.0.1» в адресах моделей в момент обращения.
# SEMA_KEY задаётся при запуске: им шифруются ключи моделей, пароли ящиков и
# токен почты. В образ и в том с данными он не попадает намеренно.
ENTRYPOINT ["/usr/local/bin/sema"]
# каталог данных один: база, вложения и служебные файлы внутри тома
CMD ["-data", "/data", "-config", "/data/config.yaml"]

# Секреты в образ не попадают: копируется только код, а конфигурация приходит
# извне через переменные окружения.

FROM golang:1.25-alpine AS builder

WORKDIR /src

# Сначала манифесты: слой с зависимостями переиспользуется, пока не меняются
# версии. Контракт приходит из прокси модулей по версии из go.mod.
COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/video-platform ./cmd

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget \
    && adduser -D -u 10001 app

WORKDIR /app

COPY --from=builder /out/video-platform /app/video-platform
COPY --from=builder /src/web /app/web

# Файл .env намеренно не копируется: секреты остались бы в слоях образа.
USER app

EXPOSE 8080

ENTRYPOINT ["/app/video-platform"]
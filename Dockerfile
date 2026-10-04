# Dockerfile собирает шлюз из локальной копии контракта.
#
# По умолчанию подтягивается опубликованная версия. Для локальной сборки
# положите контракт рядом и передайте флаг:
#
#   docker build --build-arg CONTRACT_PATH=../grpc-contract .

FROM golang:1.24-alpine AS builder

ARG CONTRACT_PATH=""

WORKDIR /src

COPY go.mod go.sum ./

# Контракт нужен до go mod download, потому что он объявлен в require.
COPY ${CONTRACT_PATH} /src/grpc-contract

RUN if [ -d /src/grpc-contract/proto ]; then \
      go mod edit -replace github.com/nikaydo/grpc-contract=/src/grpc-contract; \
    fi \
 && go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/video-platform ./cmd

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app

WORKDIR /app

COPY --from=builder /out/video-platform /app/video-platform
COPY --from=builder /src/web /app/web

# Файл .env в образ не копируется намеренно: конфигурация приходит извне
# через переменные окружения, иначе секреты попали бы в слои.
USER app

EXPOSE 8080

ENTRYPOINT ["/app/video-platform"]

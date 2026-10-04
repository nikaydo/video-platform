# Секреты в образ не попадают: копируется только код, конфигурация приходит
# извне через переменные окружения.

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

# Файл .env намеренно не копируется: секреты остались бы в слоях образа.
USER app

EXPOSE 8080

ENTRYPOINT ["/app/video-platform"]

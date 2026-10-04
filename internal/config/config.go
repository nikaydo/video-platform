// Package config загружает и проверяет конфигурацию шлюза.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// placeholder — значение-заглушка из .env.example.
const placeholder = "CHANGE_ME"

// Config содержит конфигурацию шлюза.
type Config struct {
	// HTTP-сервер.
	Host string `env:"HOST" envDefault:"0.0.0.0"`
	Port int    `env:"PORT" envDefault:"8080"`

	// Адреса gRPC-сервисов.
	AuthHost  string `env:"AUTH_HOST"     envDefault:"auth-service"`
	AuthPort  int    `env:"AUTH_PORT"     envDefault:"50051"`
	TokenHost string `env:"TOKEN_HOST"    envDefault:"api-tokens-service"`
	TokenPort int    `env:"TOKEN_PORT"    envDefault:"50052"`
	VideoHost string `env:"VIDEO_HOST"    envDefault:"video-service"`
	VideoPort int    `env:"VIDEO_PORT"    envDefault:"50053"`

	// Cookie с сессией.
	CookieName   string        `env:"COOKIE_NAME"   envDefault:"jwt"`
	CookieSecure bool          `env:"COOKIE_SECURE" envDefault:"true"`
	SessionTTL   time.Duration `env:"SESSION_TTL"   envDefault:"24h"`

	// Ограничения запросов.
	MaxRequestBytes int64         `env:"MAX_REQUEST_BYTES" envDefault:"26214400"` // 25 МиБ
	MaxUploadBytes  int64         `env:"MAX_UPLOAD_BYTES"  envDefault:"20971520"` // 20 МиБ
	RequestTimeout  time.Duration `env:"REQUEST_TIMEOUT"    envDefault:"60s"`
	GRPCTimeout     time.Duration `env:"GRPC_TIMEOUT"       envDefault:"30s"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT"   envDefault:"30s"`

	// Ограничение частоты запросов на эндпоинты авторизации.
	AuthRateLimit  int           `env:"AUTH_RATE_LIMIT" envDefault:"10"`
	AuthRateWindow time.Duration `env:"AUTH_RATE_WINDOW" envDefault:"1m"`

	// Каталог со статикой.
	StaticDir string `env:"STATIC_DIR" envDefault:"web"`
}

// Addr возвращает адрес HTTP-сервера.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// AuthAddr возвращает адрес сервиса авторизации.
func (c Config) AuthAddr() string { return fmt.Sprintf("%s:%d", c.AuthHost, c.AuthPort) }

// TokenAddr возвращает адрес сервиса API-токенов.
func (c Config) TokenAddr() string { return fmt.Sprintf("%s:%d", c.TokenHost, c.TokenPort) }

// VideoAddr возвращает адрес сервиса видео.
func (c Config) VideoAddr() string { return fmt.Sprintf("%s:%d", c.VideoHost, c.VideoPort) }

// Load читает конфигурацию и проверяет её.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(".env"); statErr == nil {
			return Config{}, fmt.Errorf("не удалось прочитать .env: %w", err)
		}
	}

	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("не удалось разобрать конфигурацию: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate проверяет значения, которые env-парсер не может проверить сам.
func (c Config) Validate() error {
	var problems []string

	if c.Port < 1 || c.Port > 65535 {
		problems = append(problems, fmt.Sprintf("PORT=%d вне диапазона 1-65535", c.Port))
	}
	// Порты проверяются попарно: два сервиса на одном порту приведут к
	// неразрешимому конфликту при запуске. Перебор всех пар, а не только
	// соседних, — иначе совпадение auth и video осталось бы незамеченным.
	ports := []struct {
		name string
		host string
		port int
	}{
		{"AUTH_HOST/PORT", c.AuthHost, c.AuthPort},
		{"TOKEN_HOST/PORT", c.TokenHost, c.TokenPort},
		{"VIDEO_HOST/PORT", c.VideoHost, c.VideoPort},
	}
	for _, svc := range ports {
		if strings.TrimSpace(svc.host) == "" {
			problems = append(problems, svc.name+": хост не задан")
		}
		if svc.port < 1 || svc.port > 65535 {
			problems = append(problems, fmt.Sprintf("%s: порт %d вне диапазона 1-65535", svc.name, svc.port))
		}
	}
	for i := 0; i < len(ports); i++ {
		for j := i + 1; j < len(ports); j++ {
			if ports[i].port == ports[j].port {
				problems = append(problems, fmt.Sprintf(
					"%s и %s не должны использовать один порт (%d)",
					ports[i].name, ports[j].name, ports[i].port))
			}
		}
	}

	if c.CookieName == placeholder || strings.TrimSpace(c.CookieName) == "" {
		problems = append(problems, "COOKIE_NAME не задан или остался заглушкой")
	}
	if c.SessionTTL <= 0 {
		problems = append(problems, "SESSION_TTL должен быть больше нуля")
	}
	if c.MaxRequestBytes < 1024 {
		problems = append(problems, "MAX_REQUEST_BYTES должен быть не меньше 1024")
	}
	if c.MaxUploadBytes <= 0 {
		problems = append(problems, "MAX_UPLOAD_BYTES должен быть больше нуля")
	}
	if c.MaxUploadBytes > c.MaxRequestBytes {
		problems = append(problems, "MAX_UPLOAD_BYTES должен быть не больше MAX_REQUEST_BYTES")
	}
	if c.AuthRateLimit < 1 {
		problems = append(problems, "AUTH_RATE_LIMIT должен быть не меньше 1")
	}
	if c.GRPCTimeout <= 0 {
		problems = append(problems, "GRPC_TIMEOUT должен быть больше нуля")
	}

	if len(problems) > 0 {
		return fmt.Errorf("некорректная конфигурация:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

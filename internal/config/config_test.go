package config

import (
	"strings"
	"testing"
)

func TestLoadAcceptsValidConfig(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load вернул ошибку: %v", err)
	}

	if cfg.Addr() != "0.0.0.0:8080" {
		t.Errorf("Addr = %q", cfg.Addr())
	}
	if cfg.AuthAddr() != "auth-service:50051" {
		t.Errorf("AuthAddr = %q", cfg.AuthAddr())
	}
	if cfg.TokenAddr() != "api-tokens-service:50052" {
		t.Errorf("TokenAddr = %q", cfg.TokenAddr())
	}
	if cfg.VideoAddr() != "video-service:50053" {
		t.Errorf("VideoAddr = %q", cfg.VideoAddr())
	}
}

func TestLoadAcceptsOverriddenAddresses(t *testing.T) {
	t.Setenv("AUTH_HOST", "localhost")
	t.Setenv("AUTH_PORT", "60051")
	t.Setenv("TOKEN_HOST", "127.0.0.1")
	t.Setenv("TOKEN_PORT", "60052")
	t.Setenv("VIDEO_HOST", "127.0.0.1")
	t.Setenv("VIDEO_PORT", "60053")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load вернул ошибку: %v", err)
	}
	if cfg.AuthAddr() != "localhost:60051" {
		t.Errorf("AuthAddr = %q", cfg.AuthAddr())
	}
}

func TestValidateRejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "70000", "-1"} {
		t.Run(port, func(t *testing.T) {
			t.Setenv("PORT", port)
			if _, err := Load(); err == nil {
				t.Fatalf("порт %s не должен приниматься", port)
			}
		})
	}
}

// TestValidateRejectsSharedPorts — два сервиса на одном порту приведут к
// неразрешимому конфликту при запуске.
func TestValidateRejectsSharedPorts(t *testing.T) {
	tests := map[string][2]string{
		"auth и tokens":  {"AUTH_PORT", "TOKEN_PORT"},
		"tokens и video": {"TOKEN_PORT", "VIDEO_PORT"},
		"auth и video":   {"AUTH_PORT", "VIDEO_PORT"},
	}
	for name, ports := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv(ports[0], "50051")
			t.Setenv(ports[1], "50051")
			if _, err := Load(); err == nil {
				t.Fatal("совпадающие порты не должны приниматься")
			}
		})
	}
}

func TestValidateRejectsEmptyServiceHost(t *testing.T) {
	t.Setenv("AUTH_HOST", "   ")
	if _, err := Load(); err == nil {
		t.Fatal("пустой хост сервиса не должен приниматься")
	}
}

func TestValidateRejectsPlaceholderCookieName(t *testing.T) {
	for _, name := range []string{"CHANGE_ME", "   "} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("COOKIE_NAME", name)
			if _, err := Load(); err == nil {
				t.Fatalf("COOKIE_NAME=%q не должен приниматься", name)
			}
		})
	}
}

func TestValidateRejectsUploadLargerThanRequest(t *testing.T) {
	// Загрузка не может быть больше запроса: иначе проверка размера тела
	// обрежет файл раньше, чем сработает ограничение на видео.
	t.Setenv("MAX_REQUEST_BYTES", "1048576")
	t.Setenv("MAX_UPLOAD_BYTES", "20971520")

	_, err := Load()
	if err == nil {
		t.Fatal("MAX_UPLOAD_BYTES больше MAX_REQUEST_BYTES не должен приниматься")
	}
	if !strings.Contains(err.Error(), "MAX_UPLOAD_BYTES") {
		t.Errorf("ошибка не упоминает поле: %v", err)
	}
}

func TestValidateRejectsNonPositiveValues(t *testing.T) {
	tests := map[string]string{
		"SESSION_TTL":       "0s",
		"GRPC_TIMEOUT":      "0s",
		"AUTH_RATE_LIMIT":   "0",
		"MAX_REQUEST_BYTES": "512",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s не должен приниматься", name, value)
			}
		})
	}
}

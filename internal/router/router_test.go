package router

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apitokens "github.com/nikaydo/grpc-contract/gen/apiToken"
	"github.com/nikaydo/grpc-contract/gen/auth"
	"github.com/nikaydo/grpc-contract/gen/video"

	"github.com/nikaydo/video-platform/internal/config"
	"github.com/nikaydo/video-platform/internal/handlers"
)

// testTimeout подставляется вместо таймаутов: точное значение в этих тестах
// не важно, важно лишь, чтобы оно было положительным.
const testTimeout = time.Minute

// stubAuth отвечает отказом на любой вызов: проверяется маршрутизация, а не
// поведение сервисов.
type stubAuth struct{ auth.AuthClient }

func (stubAuth) SignIn(_ context.Context, _ *auth.SignInRequest, _ ...grpc.CallOption) (*auth.SignInResponse, error) {
	return nil, status.Error(codes.Unauthenticated, "неверный логин или пароль")
}

// ValidateJWT подтверждает сессию. Заглушка обязана реализовывать метод явно:
// встроенный nil-интерфейс panic при вызове, и проверка Recoverer срабатывала
// бы не на том месте, ради которого написана.
func (stubAuth) ValidateJWT(_ context.Context, req *auth.ValidateJWTRequest, _ ...grpc.CallOption) (*auth.ValidateJWTResponse, error) {
	if req.GetRefresh() {
		return &auth.ValidateJWTResponse{Id: 1, Login: "nikaydo"}, nil
	}
	return &auth.ValidateJWTResponse{Id: 1, Login: "nikaydo"}, nil
}

// stubTokens — заглушка сервиса API-токенов.
type stubTokens struct{ apitokens.ApiTokenClient }

// stubVideo — заглушка сервиса видео.
type stubVideo struct{ video.VideoClient }

// panicTokens паникует при проверке токена: нужно убедиться, что паника в
// обработчике не обрушает процесс.
type panicTokens struct{ apitokens.ApiTokenClient }

func (panicTokens) Verify(_ context.Context, _ *apitokens.VerifyRequest, _ ...grpc.CallOption) (*apitokens.VerifyResponse, error) {
	panic("искусственная паника для проверки Recoverer")
}

// newTestRouter собирает роутер с заглушками сервисов.
func newTestRouter(t *testing.T, staticDir string, tokens apitokens.ApiTokenClient) http.Handler {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	h := handlers.New(stubAuth{}, tokens, stubVideo{}, config.Config{
		CookieName:      "jwt",
		CookieSecure:    true,
		SessionTTL:      testTimeout,
		MaxRequestBytes: 1 << 20,
		MaxUploadBytes:  1 << 20,
		GRPCTimeout:     testTimeout,
		AuthRateLimit:   100,
		AuthRateWindow:  testTimeout,
	}, log)

	return New(Params{Handlers: h, StaticDir: staticDir, SecureHeaders: true})
}

// writeStatic создаёт каталог статики с файлами страниц.
func writeStatic(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for name, body := range map[string]string{
		"index.html":    "страница входа",
		"register.html": "страница регистрации",
		"user.html":     "личный кабинет",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("не удалось создать %s: %v", name, err)
		}
	}
	return dir
}

// TestStaticRoutesResolve — страницы отдаются по коротким путям.
//
// Фронтенд ссылается на /login, /register и /app. Если путь не совпадает с
// именем файла, пользователь получает 404 вместо формы входа.
func TestStaticRoutesResolve(t *testing.T) {
	handler := newTestRouter(t, writeStatic(t), stubTokens{})

	for path, want := range map[string]string{
		"/":         "страница входа",
		"/login":    "страница входа",
		"/register": "страница регистрации",
		"/app":      "личный кабинет",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("код ответа %d, ожидался 200 для пути %q", rec.Code, path)
			}
			if rec.Body.String() != want {
				t.Errorf("для пути %q отдана не та страница: %q", path, rec.Body.String())
			}
		})
	}
}

// TestStaticRouteIgnoresPathInRequest — имя файла не берётся из запроса.
//
// Значение из пути не должно влиять на то, какой файл отдаётся: иначе
// последовательность вида /login/../../secret прочитала бы файл вне каталога.
func TestStaticRouteIgnoresPathInRequest(t *testing.T) {
	dir := writeStatic(t)
	handler := newTestRouter(t, dir, stubTokens{})

	secret := filepath.Join(dir, "..", "secret.txt")
	if err := os.WriteFile(secret, []byte("секрет"), 0o600); err != nil {
		t.Fatalf("не удалось создать файл-жертву: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })

	for _, path := range []string{
		"/../secret.txt",
		"/..%2Fsecret.txt",
		"/app/../secret.txt",
		"/register/../../secret.txt",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if strings.TrimSpace(rec.Body.String()) == "секрет" {
				t.Errorf("путь %q позволил прочитать файл вне каталога статики", path)
			}
		})
	}
}

func TestHealthHandler(t *testing.T) {
	handler := newTestRouter(t, "", stubTokens{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа %d, ожидался 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"status":"ok"`) {
		t.Errorf("тело ответа: %s", body)
	}
}

// TestProtectedRoutesRequireAuth — закрытые эндпоинты без сессии отвечают 401,
// а не 404 или 500.
//
// 404 означал бы, что маршрут не зарегистрирован, и поломка была бы незаметна
// до первого обращения из интерфейса.
func TestProtectedRoutesRequireAuth(t *testing.T) {
	handler := newTestRouter(t, "", stubTokens{})

	requests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/me"},
		{http.MethodPost, "/api/logout"},
		{http.MethodPost, "/api/tokens/"},
		{http.MethodGet, "/api/tokens/"},
		{http.MethodDelete, "/api/tokens/"},
		{http.MethodPost, "/api/videos"},
		{http.MethodGet, "/api/videos"},
		{http.MethodDelete, "/api/videos/some-id"},
		{http.MethodGet, "/api/videos/some-id/stream"},
	}

	for _, tt := range requests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("код ответа %d, ожидался 401", rec.Code)
			}
		})
	}
}

// TestSecurityHeaders — заголовки безопасности выставляются при включённом
// SecureHeaders.
func TestSecurityHeaders(t *testing.T) {
	handler := newTestRouter(t, "", stubTokens{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, ожидалось %q", header, got, want)
		}
	}
}

// TestRequestIDIsSet — каждый ответ содержит идентификатор запроса, по
// которому в логах находится запись.
func TestRequestIDIsSet(t *testing.T) {
	handler := newTestRouter(t, "", stubTokens{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("заголовок X-Request-ID не установлен")
	}
}

// TestPanicIsRecovered — паника в обработчике не обрушает процесс.
//
// Без Recoverer паника в горутине HTTP-сервера завершает всю программу, и
// один неверный обработчик уронил бы шлюз целиком.
func TestPanicIsRecovered(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	h := handlers.New(stubAuth{}, panicTokens{}, stubVideo{}, config.Config{
		CookieName:      "jwt",
		CookieSecure:    true,
		GRPCTimeout:     testTimeout,
		MaxRequestBytes: 1 << 20,
		AuthRateLimit:   100,
		AuthRateWindow:  testTimeout,
	}, log)
	handler := New(Params{Handlers: h})

	// Сессия есть, поэтому запрос дойдёт до проверки токена, где паника.
	req := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	req.AddCookie(&http.Cookie{Name: "jwt", Value: "token"})
	req.Header.Set("Authorization", "Bearer vpt_x")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Паника в проверке токена перехватывается Recoverer, который отвечает
	// 500. Если бы перехвата не было, тест сам завершился бы паникой.
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("код ответа %d, ожидался 500 от Recoverer", rec.Code)
	}
}

// TestStubAuthDoesNotPanicOnValidateJWT — защита от регрессии заглушки.
//
// Пока ValidateJWT не был объявлен явно, тест выше проходил, потому что
// паника возникала в заглушке, а не в проверяемом обработчике.
func TestStubAuthDoesNotPanicOnValidateJWT(t *testing.T) {
	var stub auth.AuthClient = stubAuth{}

	resp, err := stub.ValidateJWT(context.Background(), &auth.ValidateJWTRequest{Token: "t"})
	if err != nil {
		t.Fatalf("ValidateJWT вернул ошибку: %v", err)
	}
	if resp.GetId() != 1 {
		t.Errorf("Id = %d, ожидался 1", resp.GetId())
	}
}

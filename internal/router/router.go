// Package router собирает HTTP-маршруты шлюза.
package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/nikaydo/video-platform/internal/handlers"
)

// requestTimeout ограничивает суммарное время обработки запроса.
const requestTimeout = 60 * time.Second

// requestIDKey — приватный тип ключа для идентификатора запроса.
type requestIDKey struct{}

// Params — параметры сборки маршрутов.
type Params struct {
	Handlers  *handlers.Handlers
	StaticDir string
	// SecureHeaders включает строгие заголовки безопасности.
	SecureHeaders bool
}

// New собирает обработчик запросов.
func New(p Params) http.Handler {
	r := chi.NewRouter()

	r.Use(requestIDMiddleware)
	r.Use(chimw.RealIP)
	// Без Recoverer паника в обработчике обрушивает процесс.
	r.Use(chimw.Recoverer)
	r.Use(requestTimeoutMiddleware)
	r.Use(requestLogger)
	if p.SecureHeaders {
		r.Use(securityHeaders)
	}

	h := p.Handlers

	// Публичная часть: вход и регистрация под лимитом частоты.
	r.Group(func(r chi.Router) {
		r.Use(h.LimitAuth)
		r.Post("/api/signup", h.SignUp)
		r.Post("/api/signin", h.SignIn)
		// Обновление сессии не проходит через Authenticate: access-токен
		// к этому моменту уже истёк, именно ради этого эндпоинта он и нужен.
		r.Post("/api/refresh", h.Refresh)
		r.Get("/api/health", healthHandler)
	})

	// Сессия браузера.
	r.Group(func(r chi.Router) {
		r.Use(h.Authenticate)

		r.Post("/api/logout", h.Logout)
		r.Get("/api/me", h.Me)

		r.Route("/api/tokens", func(r chi.Router) {
			r.Post("/", h.CreateToken)
			r.Get("/", h.ListTokens)
			r.Delete("/", h.DeleteToken)
		})

		// Операции с видео требуют и сессии, и API-токена: сессия
		// определяет владельца, токен подтверждает, что запрос машинный.
		r.Group(func(r chi.Router) {
			r.Use(h.RequireAPIKey)

			r.Post("/api/videos", h.UploadVideo)
			r.Get("/api/videos", h.ListVideos)
			r.Delete("/api/videos/{videoID}", h.DeleteVideo)
			r.Get("/api/videos/{videoID}/stream", h.StreamVideo)
		})
	})

	if p.StaticDir != "" {
		// Страницы отдаются по коротким путям, а не по именам файлов:
		// переименование index.html в login.html не должно ломать ссылки
		// внутри фронтенда.
		for path, file := range map[string]string{
			"/":         "index.html",
			"/login":    "index.html",
			"/register": "register.html",
			"/app":      "user.html",
		} {
			r.Get(path, serveStatic(p.StaticDir, file))
		}
		r.Handle("/*", http.FileServer(http.Dir(p.StaticDir)))
	}

	return r
}

// serveStatic возвращает обработчик, отдающий указанный файл каталога.
//
// Путь к файлу собирается через filepath.Join из двух констант, а не из
// значения запроса, поэтому подделать имя файла параметром URL нельзя.
func serveStatic(dir, file string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(dir, file))
	}
}

// healthHandler сообщает о готовности шлюза.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// securityHeaders выставляет заголовки безопасности.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// connect-src 'self' запрещает браузеру обращаться к посторонним
		// адресам, что ограничивает кражу данных через XSS.
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; media-src 'self'")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		next.ServeHTTP(w, r)
	})
}

// requestTimeoutMiddleware ограничивает время обработки запроса.
func requestTimeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestIDMiddleware присваивает запросу идентификатор и кладёт его в контекст.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chimw.GetReqID(r.Context())
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newRequestID генерирует идентификатор запроса.
func newRequestID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(buf)
}

// statusRecorder запоминает код ответа для журналирования.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

// WriteHeader запоминает код и передаёт его дальше.
func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Write считает объём ответа и передаёт его дальше.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush передаёт буферизованные данные клиенту.
//
// Нужен для потоковой передачи видео: без него ответ накапливался бы в
// буфере и дошёл до клиента только целиком.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requestLogger пишет по одной структурированной записи на запрос.
func requestLogger(next http.Handler) http.Handler {
	log := slog.Default()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 0}

		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}

		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400:
			level = slog.LevelWarn
		}

		id, _ := r.Context().Value(requestIDKey{}).(string)

		log.LogAttrs(r.Context(), level, "http_request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("duration", time.Since(start)),
			slog.String("request_id", id),
		)
	})
}

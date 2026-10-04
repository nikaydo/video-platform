// Package handlers содержит HTTP-обработчики шлюза.
package handlers

import (
	"context"
	"log/slog"

	apitokens "github.com/nikaydo/grpc-contract/gen/apiToken"
	"github.com/nikaydo/grpc-contract/gen/auth"
	"github.com/nikaydo/grpc-contract/gen/video"

	"github.com/nikaydo/video-platform/internal/config"
)

// Handlers связывает зависимости обработчиков.
type Handlers struct {
	Auth   auth.AuthClient
	Tokens apitokens.ApiTokenClient
	Video  video.VideoClient

	Cfg config.Config
	Log *slog.Logger

	// rateLimiter ограничивает частоту запросов к эндпоинтам авторизации.
	rateLimiter *limiter
}

// New создаёт набор обработчиков.
func New(auth auth.AuthClient, tokens apitokens.ApiTokenClient, vid video.VideoClient, cfg config.Config, log *slog.Logger) *Handlers {
	if log == nil {
		log = slog.Default()
	}
	return &Handlers{
		Auth:        auth,
		Tokens:      tokens,
		Video:       vid,
		Cfg:         cfg,
		Log:         log,
		rateLimiter: newLimiter(cfg.AuthRateLimit, cfg.AuthRateWindow),
	}
}

// session — данные аутентифицированного пользователя в контексте запроса.
type session struct {
	// UserID и Login приходят из проверенного access-токена. Обработчики
	// берут их отсюда, а не из тела запроса: иначе принадлежность ресурса
	// можно было бы подделать.
	UserID int32
	Login  string
	// APIKey — значение заголовка Authorization, если запрос пришёл с ним.
	APIKey string
	// FamilyID — семейство сессии для ротации refresh-токена.
	FamilyID string
}

type sessionKey struct{}

func withSession(ctx context.Context, s session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// sessionFrom извлекает данные сессии из контекста.
func sessionFrom(ctx context.Context) (session, bool) {
	s, ok := ctx.Value(sessionKey{}).(session)
	return s, ok
}

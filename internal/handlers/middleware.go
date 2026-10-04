package handlers

import (
	"context"
	"net/http"

	apitokens "github.com/nikaydo/grpc-contract/gen/apiToken"
	"github.com/nikaydo/grpc-contract/gen/auth"
)

// callCtx возвращает контекст вызова gRPC с таймаутом.
//
// Таймаут обязателен: без него запрос к зависшему сервису держал бы
// HTTP-соединение до конца соединения клиента.
func (h *Handlers) callCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), h.Cfg.GRPCTimeout)
}

// Authenticate проверяет access-токен в cookie.
//
// Идентификатор пользователя кладётся в контекст запроса: обработчики берут его
// оттуда, а не из тела запроса. Так подделать принадлежность ресурса нельзя.
func (h *Handlers) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		cookie, err := r.Cookie(h.Cfg.CookieName)
		if err != nil || cookie.Value == "" {
			fail(ctx, w, h.Log, http.StatusUnauthorized, "no_token", "требуется авторизация")
			return
		}

		callCtx, cancel := h.callCtx(r)
		defer cancel()

		resp, err := h.Auth.ValidateJWT(callCtx, &auth.ValidateJWTRequest{
			Token:   cookie.Value,
			Refresh: false,
		})
		if err != nil {
			forwardError(ctx, w, h.Log, err)
			return
		}

		// Поле expired игнорировалось раньше, из-за чего просроченный токен
		// проходил проверку.
		if resp.GetExpired() {
			w.Header().Set("X-Auth-Reason", "expired")
			fail(ctx, w, h.Log, http.StatusUnauthorized, "token_expired", "срок действия токена истёк")
			return
		}

		s := session{
			UserID: resp.GetId(),
			Login:  resp.GetLogin(),
		}
		if rc, rErr := r.Cookie(refreshCookieName); rErr == nil && rc.Value != "" {
			s.FamilyID, _ = splitFamily(rc.Value)
		}

		next.ServeHTTP(w, r.WithContext(withSession(ctx, s)))
	})
}

// RequireAPIKey проверяет API-токен и кладёт его значение в контекст.
//
// Применяется после Authenticate: сессия определяет владельца, а токен
// подтверждает, что запрос машинный.
//
// Токен принимается в заголовке Authorization. Раньше он передавался
// параметром query-строки, из-за чего попадал в журналы веб-сервера и
// прокси, в историю браузера и в заголовок Referer при переходах.
func (h *Handlers) RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		raw := bearerToken(r)
		if raw == "" {
			fail(ctx, w, h.Log, http.StatusUnauthorized, "no_api_token",
				"укажите API-токен в заголовке Authorization: Bearer <токен>")
			return
		}

		callCtx, cancel := h.callCtx(r)
		defer cancel()

		// Владелец берётся из сессии браузера: сервис токенов проверяет
		// сам токен, но не сообщает, кому он принадлежит.
		s, ok := sessionFrom(ctx)
		if !ok {
			fail(ctx, w, h.Log, http.StatusUnauthorized, "no_session", "требуется авторизация")
			return
		}

		resp, err := h.Tokens.Verify(callCtx, &apitokens.VerifyRequest{Token: raw})
		if err != nil {
			forwardError(ctx, w, h.Log, err)
			return
		}
		if !resp.GetResult() {
			fail(ctx, w, h.Log, http.StatusUnauthorized, "api_token_invalid", "недействительный API-токен")
			return
		}

		next.ServeHTTP(w, r.WithContext(withSession(ctx, session{
			UserID:   s.UserID,
			Login:    s.Login,
			APIKey:   raw,
			FamilyID: s.FamilyID,
		})))
	})
}

// bearerToken извлекает токен из заголовка Authorization.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return ""
	}
	return header[len(prefix):]
}

// LimitAuth ограничивает частоту запросов к эндпоинтам авторизации.
func (h *Handlers) LimitAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if ok, retryAfter := h.rateLimiter.allow(clientKey(r)); !ok {
			w.Header().Set("Retry-After", formatSeconds(retryAfter))
			fail(ctx, w, h.Log, http.StatusTooManyRequests, "rate_limited",
				"слишком много попыток, попробуйте позже")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientKey определяет источник запроса по заголовку X-Real-IP.
//
// За обратным прокси значение RemoteAddr — адрес прокси, и без заголовка все
// запросы попали бы в один счётчик.
func clientKey(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

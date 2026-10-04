package handlers

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nikaydo/grpc-contract/gen/auth"
)

// refreshCookieName — имя cookie с refresh-токеном.
//
// Refresh-токен хранится в HttpOnly-cookie, а не в теле ответа: иначе он был
// бы доступен JavaScript и мог быть украден через XSS.
const refreshCookieName = "refresh"

// credentials — тело запроса регистрации и входа.
type credentials struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// userResponse — публичные сведения о пользователе.
type userResponse struct {
	ID    int32  `json:"id"`
	Login string `json:"login"`
}

// SignUp регистрирует пользователя.
func (h *Handlers) SignUp(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	var creds credentials
	if err := decodeJSON(w, r, &creds, h.Cfg.MaxRequestBytes); err != nil {
		failErr(ctx, w, h.Log, http.StatusBadRequest, "bad_request", "не удалось прочитать тело запроса", err)
		return
	}

	resp, err := h.Auth.SignUp(ctx, &auth.SignUpRequest{
		Login:    strings.TrimSpace(creds.Login),
		Password: creds.Password,
	})
	if err != nil {
		forwardError(ctx, w, h.Log, err)
		return
	}

	// Сессия выдаётся сразу после регистрации: клиенту не нужно отправлять
	// пароль ещё раз, чтобы войти.
	if resp.GetToken() == "" || resp.GetRefreshToken() == "" {
		fail(ctx, w, h.Log, http.StatusInternalServerError, "session_incomplete", "сервис не вернул токены")
		return
	}
	if err := h.setSessionCookies(w, resp.GetToken(), resp.GetRefreshToken()); err != nil {
		failErr(ctx, w, h.Log, http.StatusInternalServerError, "session_failed", "не удалось создать сессию", err)
		return
	}

	writeJSON(w, http.StatusCreated, userResponse{ID: resp.GetUserId(), Login: strings.TrimSpace(creds.Login)})
}

// SignIn проверяет пароль и выдаёт сессию.
//
// Раньше токен возвращался и в cookie, и в теле ответа. Тело ответа отменено:
// благодаря HttpOnly-cookie токен недоступен JavaScript, а выдача в теле
// полностью обесценивала эту защиту.
func (h *Handlers) SignIn(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	var creds credentials
	if err := decodeJSON(w, r, &creds, h.Cfg.MaxRequestBytes); err != nil {
		failErr(ctx, w, h.Log, http.StatusBadRequest, "bad_request", "не удалось прочитать тело запроса", err)
		return
	}

	resp, err := h.Auth.SignIn(ctx, &auth.SignInRequest{
		Login:    strings.TrimSpace(creds.Login),
		Password: creds.Password,
	})
	if err != nil {
		forwardError(ctx, w, h.Log, err)
		return
	}

	if resp.GetToken() == "" || resp.GetRefreshToken() == "" {
		fail(ctx, w, h.Log, http.StatusInternalServerError, "session_incomplete", "сервис не вернул токены")
		return
	}

	if err := h.setSessionCookies(w, resp.GetToken(), resp.GetRefreshToken()); err != nil {
		failErr(ctx, w, h.Log, http.StatusInternalServerError, "session_failed", "не удалось создать сессию", err)
		return
	}

	writeJSON(w, http.StatusOK, userResponse{})
}

// Refresh обновляет сессию по refresh-токену.
//
// Ротация: предъявленный токен заменяется новым. Если пришёл уже отозванный
// токен, сервис авторизации сообщает об этом, и сессия завершается.
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	cookie, err := r.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		fail(ctx, w, h.Log, http.StatusUnauthorized, "no_refresh_token", "отсутствует refresh-токен")
		return
	}

	// По refresh-токену узнаём пользователя, чтобы передать его идентификатор
	// в запрос ротации.
	user, err := h.Auth.ValidateJWT(ctx, &auth.ValidateJWTRequest{
		Token:   cookie.Value,
		Refresh: true,
	})
	if err != nil {
		h.clearSession(w)
		forwardError(ctx, w, h.Log, err)
		return
	}
	if user.GetExpired() {
		h.clearSession(w)
		fail(ctx, w, h.Log, http.StatusUnauthorized, "refresh_expired", "срок действия refresh-токена истёк")
		return
	}

	family, _ := splitFamily(cookie.Value)
	resp, err := h.Auth.CreateTokens(ctx, &auth.CreateTokensRequest{
		Id:         user.GetId(),
		RotateFrom: hashToken(cookie.Value),
		FamilyId:   family,
	})
	if err != nil {
		// Повторное использование отозванного токена — признак утечки.
		h.clearSession(w)
		h.Log.Warn("ротация refresh-токена отклонена",
			"user_id", user.GetId(),
			"err", err,
		)
		forwardError(ctx, w, h.Log, err)
		return
	}

	if err := h.setSessionCookies(w, resp.GetJwtToken(), resp.GetRefreshToken()); err != nil {
		failErr(ctx, w, h.Log, http.StatusInternalServerError, "session_failed", "не удалось обновить сессию", err)
		return
	}

	writeJSON(w, http.StatusOK, userResponse{ID: user.GetId(), Login: user.GetLogin()})
}

// Logout завершает сессию на стороне клиента.
//
// Токен отзывается на сервере авторизации через ротацию семейства: без этого
// украденный refresh-токен продолжал бы работать до истечения срока.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	if cookie, err := r.Cookie(refreshCookieName); err == nil && cookie.Value != "" {
		user, uErr := h.Auth.ValidateJWT(ctx, &auth.ValidateJWTRequest{
			Token:   cookie.Value,
			Refresh: true,
		})
		if uErr == nil && !user.GetExpired() {
			// Ротация со пустым новым токеном невозможна, поэтому семейство
			// отзывается через отказ от ротации: сервис отметит токен
			// отозванным и выдаст замену, которая тут же будет проигнорирована.
			family, _ := splitFamily(cookie.Value)
			if _, rErr := h.Auth.CreateTokens(ctx, &auth.CreateTokensRequest{
				Id:         user.GetId(),
				RotateFrom: hashToken(cookie.Value),
				FamilyId:   family,
			}); rErr != nil {
				h.Log.Info("не удалось отозвать токен при выходе", "err", rErr)
			}
		}
	}

	h.clearSession(w)
	w.WriteHeader(http.StatusNoContent)
}

// Me возвращает сведения о текущем пользователе.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	s, ok := sessionFrom(ctx)
	if !ok {
		fail(ctx, w, h.Log, http.StatusUnauthorized, "no_session", "требуется авторизация")
		return
	}
	writeJSON(w, http.StatusOK, userResponse{ID: s.UserID, Login: s.Login})
}

// setSessionCookies устанавливает cookie с access- и refresh-токеном.
func (h *Handlers) setSessionCookies(w http.ResponseWriter, accessToken, refreshToken string) error {
	if accessToken == "" {
		return errors.New("access-токен пуст")
	}
	expires := time.Now().Add(h.Cfg.SessionTTL)

	http.SetCookie(w, h.buildCookie(h.Cfg.CookieName, accessToken, expires))
	http.SetCookie(w, h.buildCookie(refreshCookieName, refreshToken, expires))
	return nil
}

// buildCookie собирает cookie с защитными флагами.
func (h *Handlers) buildCookie(name, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:  name,
		Value: value,
		Path:  "/",
		// Сессия живёт дольше access-токена: она продлевается ротацией
		// refresh-токена, поэтому cookie не должна исчезать вместе с ним.
		Expires: expires,
		// HttpOnly закрывает доступ из JavaScript.
		HttpOnly: true,
		// Secure не даёт отправить cookie по http.
		Secure: h.Cfg.CookieSecure,
		// SameSite=Strict не даёт отправить cookie на сайт, инициировавший
		// переход: это закрывает CSRF через межсайтовые формы.
		SameSite: http.SameSiteStrictMode,
	}
}

// clearSession удаляет cookie сессии.
func (h *Handlers) clearSession(w http.ResponseWriter) {
	expired := &http.Cookie{
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.Cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
	c := *expired
	c.Name = h.Cfg.CookieName
	http.SetCookie(w, &c)

	r := *expired
	r.Name = refreshCookieName
	http.SetCookie(w, &r)
}

// hashToken возвращает хеш refresh-токена для ротации.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// splitFamily извлекает идентификатор семейства из refresh-токена.
//
// Формат токена: <семейство>.<секрет>. Разделение нужно для ротации: сервис
// авторизации должен знать, в какое семейство продолжать выдачу.
func splitFamily(token string) (family, secret string) {
	if i := strings.IndexByte(token, '.'); i >= 0 {
		return token[:i], token[i+1:]
	}
	return "", token
}

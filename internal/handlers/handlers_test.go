package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
)

// fakeAuth подменяет клиента gRPC-сервиса авторизации.
type fakeAuth struct {
	signUpResp   *auth.SignUpResponse
	signInResp   *auth.SignInResponse
	validateResp *auth.ValidateJWTResponse
	validateErr  error
	signInErr    error

	lastValidateRefresh bool
	lastRotateFrom      string
}

func (f *fakeAuth) SignUp(_ context.Context, _ *auth.SignUpRequest, _ ...grpc.CallOption) (*auth.SignUpResponse, error) {
	return f.signUpResp, nil
}

func (f *fakeAuth) SignIn(_ context.Context, _ *auth.SignInRequest, _ ...grpc.CallOption) (*auth.SignInResponse, error) {
	return f.signInResp, f.signInErr
}

func (f *fakeAuth) ValidateJWT(_ context.Context, req *auth.ValidateJWTRequest, _ ...grpc.CallOption) (*auth.ValidateJWTResponse, error) {
	f.lastValidateRefresh = req.GetRefresh()
	return f.validateResp, f.validateErr
}

func (f *fakeAuth) CreateTokens(_ context.Context, req *auth.CreateTokensRequest, _ ...grpc.CallOption) (*auth.CreateTokensResponse, error) {
	f.lastRotateFrom = req.GetRotateFrom()
	return &auth.CreateTokensResponse{
		JwtToken:     "rotated-access-token",
		RefreshToken: "rotated-refresh-token",
	}, nil
}

// CheckUser не используется шлюзом, но требуется интерфейсом клиента.
func (f *fakeAuth) CheckUser(_ context.Context, _ *auth.CheckUserRequest, _ ...grpc.CallOption) (*auth.CheckUserResponse, error) {
	return nil, errors.New("не используется")
}

// fakeTokens подменяет gRPC-сервис API-токенов.
type fakeTokens struct {
	verifyResult bool
	createResp   *apitokens.CreateResponse
	lastUserID   int32
}

func (f *fakeTokens) Verify(_ context.Context, _ *apitokens.VerifyRequest, _ ...grpc.CallOption) (*apitokens.VerifyResponse, error) {
	return &apitokens.VerifyResponse{Result: f.verifyResult}, nil
}

func (f *fakeTokens) Create(_ context.Context, req *apitokens.CreateRequest, _ ...grpc.CallOption) (*apitokens.CreateResponse, error) {
	f.lastUserID = req.GetUserId()
	return f.createResp, nil
}

func (f *fakeTokens) Delete(_ context.Context, req *apitokens.DeleteRequest, _ ...grpc.CallOption) (*apitokens.DeleteResponse, error) {
	f.lastUserID = req.GetUserId()
	return &apitokens.DeleteResponse{Result: true}, nil
}

func (f *fakeTokens) Get(_ context.Context, req *apitokens.GetRequest, _ ...grpc.CallOption) (*apitokens.GetResponse, error) {
	f.lastUserID = req.GetUserId()
	return &apitokens.GetResponse{Tokens: &apitokens.Tokens{}}, nil
}

// fakeVideo подменяет gRPC-сервис видео.
type fakeVideo struct {
	lastUserID int32
	listResp   *video.GetResponse
}

func (f *fakeVideo) Add(_ context.Context, req *video.AddRequest, _ ...grpc.CallOption) (*video.AddResponse, error) {
	f.lastUserID = req.GetUserId()
	return &video.AddResponse{Result: true}, nil
}

func (f *fakeVideo) Get(_ context.Context, req *video.GetRequest, _ ...grpc.CallOption) (*video.GetResponse, error) {
	f.lastUserID = req.GetUserId()
	if f.listResp != nil {
		return f.listResp, nil
	}
	return &video.GetResponse{Video: &video.Videos{Video: []*video.SavedVideo{
		{Uuid: "abc", Title: "ролик"},
	}}}, nil
}

func (f *fakeVideo) Delete(_ context.Context, req *video.DeleteRequest, _ ...grpc.CallOption) (*video.DeleteResponse, error) {
	f.lastUserID = req.GetUserId()
	return &video.DeleteResponse{Result: true}, nil
}

func (f *fakeVideo) Stream(_ context.Context, _ *video.StreamRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[video.StreamResponse], error) {
	return nil, errors.New("поток не используется в тестах")
}

// testConfig возвращает конфигурацию для тестов.
func testConfig() config.Config {
	return config.Config{
		CookieName:      "jwt",
		CookieSecure:    true,
		SessionTTL:      time.Hour,
		MaxRequestBytes: 1024 * 1024,
		MaxUploadBytes:  1024 * 1024,
		GRPCTimeout:     time.Second,
		AuthRateLimit:   100,
		AuthRateWindow:  time.Minute,
	}
}

// newTestHandlers собирает обработчики с подменёнными сервисами.
func newTestHandlers(fa *fakeAuth, ft *fakeTokens, fv *fakeVideo) *Handlers {
	return New(fa, ft, fv, testConfig(), slogDiscard())
}

// TestSignInDoesNotLeakTokensInBody — регрессия на утечку токенов.
//
// Раньше access-токен возвращался и в cookie, и в теле ответа. Тело ответа
// доступно JavaScript, поэтому такая выдача полностью обесценивала HttpOnly.
func TestSignInDoesNotLeakTokensInBody(t *testing.T) {
	fa := &fakeAuth{signInResp: &auth.SignInResponse{
		Token:        "access-token-value",
		RefreshToken: "refresh-token-value",
	}}
	h := newTestHandlers(fa, &fakeTokens{}, &fakeVideo{})

	body := strings.NewReader(`{"login":"nikaydo","password":"correct-horse-battery"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/signin", body)
	rec := httptest.NewRecorder()

	h.SignIn(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа %d, ожидался 200: %s", rec.Code, rec.Body.String())
	}

	payload := rec.Body.String()
	for _, secret := range []string{"access-token-value", "refresh-token-value"} {
		if strings.Contains(payload, secret) {
			t.Fatalf("токен %q найден в теле ответа: %s", secret, payload)
		}
	}
	// Имя пользователя возвращать можно — это не секрет.
	if !strings.Contains(payload, `"`+"{}"+`"`) && payload != "{}\n" {
		t.Logf("тело ответа: %s", payload)
	}

	// Токены обязаны быть в cookie.
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == h.Cfg.CookieName && c.Value == "access-token-value" {
			if !c.HttpOnly {
				t.Error("cookie с access-токеном не помечена HttpOnly")
			}
			if !c.Secure {
				t.Error("cookie с access-токеном не помечена Secure")
			}
			if c.SameSite != http.SameSiteStrictMode {
				t.Errorf("SameSite = %v, ожидался StrictMode", c.SameSite)
			}
			found = true
		}
		if c.Name == refreshCookieName && c.Value == "refresh-token-value" {
			if !c.HttpOnly {
				t.Error("cookie с refresh-токеном не помечена HttpOnly")
			}
			found = true
		}
	}
	if !found {
		t.Error("токены не установлены в cookie")
	}
}

func TestSignInRejectsEmptyTokens(t *testing.T) {
	// Сервис может ответить без токенов: пустая сессия опаснее явной ошибки.
	fa := &fakeAuth{signInResp: &auth.SignInResponse{}}
	h := newTestHandlers(fa, &fakeTokens{}, &fakeVideo{})

	req := httptest.NewRequest(http.MethodPost, "/api/signin",
		strings.NewReader(`{"login":"a","password":"b"}`))
	rec := httptest.NewRecorder()

	h.SignIn(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("код ответа %d, ожидался 500", rec.Code)
	}
}

func TestSignInForwardsServiceError(t *testing.T) {
	fa := &fakeAuth{signInErr: status.Error(codes.Unauthenticated, "неверный логин или пароль")}
	h := newTestHandlers(fa, &fakeTokens{}, &fakeVideo{})

	req := httptest.NewRequest(http.MethodPost, "/api/signin",
		strings.NewReader(`{"login":"a","password":"b"}`))
	rec := httptest.NewRecorder()

	h.SignIn(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("код ответа %d, ожидался 401", rec.Code)
	}
}

// TestAuthenticateRejectsExpiredToken — регрессия на приём просроченного токена.
//
// Поле expired в ответе сервиса игнорировалось, поэтому истёкший access-токен
// проходил проверку.
func TestAuthenticateRejectsExpiredToken(t *testing.T) {
	fa := &fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 1, Login: "nikaydo", Expired: true}}
	h := newTestHandlers(fa, &fakeTokens{}, &fakeVideo{})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("обработчик не должен вызываться для просроченного токена")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: h.Cfg.CookieName, Value: "token"})
	rec := httptest.NewRecorder()

	h.Authenticate(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("код ответа %d, ожидался 401", rec.Code)
	}
	if rec.Header().Get("X-Auth-Reason") != "expired" {
		t.Error("не установлен заголовок X-Auth-Reason: клиент не сможет обновить сессию")
	}
}

func TestAuthenticatePutsSessionInContext(t *testing.T) {
	fa := &fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 42, Login: "nikaydo", Expired: false}}
	h := newTestHandlers(fa, &fakeTokens{}, &fakeVideo{})

	var got session
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = sessionFrom(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: h.Cfg.CookieName, Value: "token"})
	rec := httptest.NewRecorder()

	h.Authenticate(next).ServeHTTP(rec, req)

	if got.UserID != 42 {
		t.Errorf("идентификатор пользователя %d, ожидался 42", got.UserID)
	}
	if got.Login != "nikaydo" {
		t.Errorf("логин %q", got.Login)
	}
}

func TestAuthenticateRejectsMissingCookie(t *testing.T) {
	h := newTestHandlers(&fakeAuth{}, &fakeTokens{}, &fakeVideo{})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("обработчик не должен вызываться без cookie")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()

	h.Authenticate(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("код ответа %d, ожидался 401", rec.Code)
	}
}

// TestRequireAPIKeyRejectsMissingHeader — регрессия на токен в query-строке.
//
// Раньше токен принимался параметром `?token=`, из-за чего он попадал в
// журналы веб-сервера и прокси, в историю браузера и в заголовок Referer.
func TestRequireAPIKeyRejectsMissingHeader(t *testing.T) {
	h := newTestHandlers(
		&fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 1, Login: "n", Expired: false}},
		&fakeTokens{verifyResult: true},
		&fakeVideo{},
	)

	// Запрос с параметром query, как было раньше.
	req := httptest.NewRequest(http.MethodGet, "/api/videos?token=vpt_secretvalue", nil)
	req.AddCookie(&http.Cookie{Name: h.Cfg.CookieName, Value: "token"})
	rec := httptest.NewRecorder()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("обработчик не должен вызываться без заголовка Authorization")
	})

	// Сначала аутентификация, чтобы сессия была в контексте.
	inner := h.RequireAPIKey(next)
	h.Authenticate(inner).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("код ответа %d, ожидался 401: токен в query-строке приниматься не должен", rec.Code)
	}
}

func TestRequireAPIKeyAcceptsBearerHeader(t *testing.T) {
	ft := &fakeTokens{verifyResult: true}
	h := newTestHandlers(
		&fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 1, Login: "n", Expired: false}},
		ft,
		&fakeVideo{},
	)

	var got session
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = sessionFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	req.Header.Set("Authorization", "Bearer vpt_secretvalue")
	req.AddCookie(&http.Cookie{Name: h.Cfg.CookieName, Value: "token"})
	rec := httptest.NewRecorder()

	h.Authenticate(h.RequireAPIKey(next)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа %d, ожидался 200: %s", rec.Code, rec.Body.String())
	}
	if got.APIKey != "vpt_secretvalue" {
		t.Errorf("токен в контексте %q", got.APIKey)
	}
}

func TestRequireAPIKeyRejectsInvalidToken(t *testing.T) {
	h := newTestHandlers(
		&fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 1, Login: "n", Expired: false}},
		&fakeTokens{verifyResult: false},
		&fakeVideo{},
	)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("обработчик не должен вызываться с недействительным токеном")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	req.Header.Set("Authorization", "Bearer vpt_wrong")
	req.AddCookie(&http.Cookie{Name: h.Cfg.CookieName, Value: "token"})
	rec := httptest.NewRecorder()

	h.Authenticate(h.RequireAPIKey(next)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("код ответа %d, ожидался 401", rec.Code)
	}
}

// TestOwnershipComesFromSession — регрессия на подмену владельца.
func TestOwnershipComesFromSession(t *testing.T) {
	ft := &fakeTokens{verifyResult: true, createResp: &apitokens.CreateResponse{Token: "vpt_new"}}
	fv := &fakeVideo{}
	h := newTestHandlers(
		&fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 7, Login: "owner", Expired: false}},
		ft,
		fv,
	)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.CreateToken(w, r)
	})

	// Тело не содержит идентификатора пользователя: обработчик берёт
	// владельца из сессии, поэтому указать его в запросе невозможно.
	req := httptest.NewRequest(http.MethodPost, "/api/tokens/",
		strings.NewReader(`{"name":"для CI"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: h.Cfg.CookieName, Value: "token"})
	rec := httptest.NewRecorder()

	h.Authenticate(next).ServeHTTP(rec, req)

	if ft.lastUserID != 7 {
		t.Errorf("сервису передан владелец %d, ожидался 7 из сессии", ft.lastUserID)
	}
}

// TestUnknownFieldIsRejected — защита от подмены полей в теле запроса.
//
// Разбор использует DisallowUnknownFields: поле userId в теле отклоняется,
// поэтому подделать владельца через тело запроса нельзя даже случайно.
func TestUnknownFieldIsRejected(t *testing.T) {
	ft := &fakeTokens{createResp: &apitokens.CreateResponse{Token: "vpt_new"}}
	h := newTestHandlers(
		&fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 7, Login: "owner", Expired: false}},
		ft,
		&fakeVideo{},
	)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.CreateToken(w, r)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/tokens/",
		strings.NewReader(`{"userId":999}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: h.Cfg.CookieName, Value: "token"})
	rec := httptest.NewRecorder()

	h.Authenticate(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("код ответа %d, ожидался 400: неизвестное поле должно отклоняться", rec.Code)
	}
	if ft.lastUserID != 0 {
		t.Error("сервис не должен вызываться при некорректном теле запроса")
	}
}

func TestListVideosUsesSessionOwner(t *testing.T) {
	fv := &fakeVideo{}
	h := newTestHandlers(
		&fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 11, Login: "owner", Expired: false}},
		&fakeTokens{verifyResult: true},
		fv,
	)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ListVideos(w, r)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/videos?name=abc", nil)
	req.Header.Set("Authorization", "Bearer vpt_x")
	req.AddCookie(&http.Cookie{Name: h.Cfg.CookieName, Value: "token"})
	rec := httptest.NewRecorder()

	h.Authenticate(h.RequireAPIKey(next)).ServeHTTP(rec, req)

	if fv.lastUserID != 11 {
		t.Errorf("сервису видео передан владелец %d, ожидался 11", fv.lastUserID)
	}
}

func TestRefreshRotatesSession(t *testing.T) {
	fa := &fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 5, Login: "n", Expired: false}}
	h := newTestHandlers(fa, &fakeTokens{}, &fakeVideo{})

	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: "family123.secret456"})
	rec := httptest.NewRecorder()

	h.Refresh(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа %d, ожидался 200: %s", rec.Code, rec.Body.String())
	}
	if !fa.lastValidateRefresh {
		t.Error("refresh-токен должен проверяться как refresh")
	}
	if fa.lastRotateFrom == "" {
		t.Error("ротация должна передавать хеш предыдущего токена")
	}
	// В ротацию передаётся хеш, а не сам токен.
	if strings.Contains(fa.lastRotateFrom, "secret456") {
		t.Error("в ротацию передан сам токен вместо хеша")
	}
}

func TestRefreshRejectsMissingToken(t *testing.T) {
	h := newTestHandlers(&fakeAuth{}, &fakeTokens{}, &fakeVideo{})

	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rec := httptest.NewRecorder()

	h.Refresh(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("код ответа %d, ожидался 401", rec.Code)
	}
}

func TestRefreshRejectsExpiredRefreshToken(t *testing.T) {
	fa := &fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 5, Expired: true}}
	h := newTestHandlers(fa, &fakeTokens{}, &fakeVideo{})

	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: "f.s"})
	rec := httptest.NewRecorder()

	h.Refresh(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("код ответа %d, ожидался 401", rec.Code)
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Error("cookie сессии не должны удаляться при отказе")
	}
}

func TestLogoutClearsCookies(t *testing.T) {
	fa := &fakeAuth{validateResp: &auth.ValidateJWTResponse{Id: 5, Login: "n", Expired: false}}
	h := newTestHandlers(fa, &fakeTokens{}, &fakeVideo{})

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: "f.s"})
	rec := httptest.NewRecorder()

	h.Logout(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("код ответа %d, ожидался 204", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge >= 0 {
			t.Errorf("cookie %q не удалена: MaxAge = %d", c.Name, c.MaxAge)
		}
	}
}

func TestSplitFamily(t *testing.T) {
	tests := []struct {
		token      string
		wantFamily string
	}{
		{"family123.secret456", "family123"},
		{"безразделителя", ""},
		{".onlysecret", ""},
		{"a.b.c", "a"},
	}

	for _, tt := range tests {
		family, _ := splitFamily(tt.token)
		if family != tt.wantFamily {
			t.Errorf("splitFamily(%q) = %q, ожидалось %q", tt.token, family, tt.wantFamily)
		}
	}
}

func TestHashTokenIsNotReversible(t *testing.T) {
	const raw = "family.secret"
	hashed := hashToken(raw)

	if hashed == raw {
		t.Error("хеш совпадает с токеном")
	}
	if strings.Contains(hashed, "secret") {
		t.Error("хеш содержит часть токена")
	}
	if hashToken(raw) != hashed {
		t.Error("хеш не воспроизводится")
	}
}

// TestGRPCErrorsDoNotLeakDetails — внутренние ошибки не должны попадать к клиенту.
func TestGRPCErrorsDoNotLeakDetails(t *testing.T) {
	// Сообщение внутренней ошибки содержит детали реализации: названия
	// таблиц, колонок, адреса.
	internal := status.Error(codes.Internal,
		"pq: ошибка в relation \"users\" column \"password_hash\": сервер 10.0.0.5 недоступен")

	fa := &fakeAuth{signInErr: internal}
	h := newTestHandlers(fa, &fakeTokens{}, &fakeVideo{})

	req := httptest.NewRequest(http.MethodPost, "/api/signin",
		strings.NewReader(`{"login":"a","password":"b"}`))
	rec := httptest.NewRecorder()

	h.SignIn(rec, req)

	body := rec.Body.String()
	for _, leak := range []string{"users", "password_hash", "10.0.0.5", "relation"} {
		if strings.Contains(body, leak) {
			t.Fatalf("в ответе клиенту просочились детали ошибки (%q): %s", leak, body)
		}
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("код ответа %d, ожидался 500", rec.Code)
	}
}

func TestGRPCErrorCodeMapping(t *testing.T) {
	tests := []struct {
		grpcCode codes.Code
		wantHTTP int
	}{
		{codes.InvalidArgument, http.StatusBadRequest},
		{codes.Unauthenticated, http.StatusUnauthorized},
		{codes.PermissionDenied, http.StatusForbidden},
		{codes.NotFound, http.StatusNotFound},
		{codes.AlreadyExists, http.StatusConflict},
		{codes.ResourceExhausted, http.StatusTooManyRequests},
		{codes.DeadlineExceeded, http.StatusGatewayTimeout},
		{codes.Unavailable, http.StatusServiceUnavailable},
		{codes.Internal, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.grpcCode.String(), func(t *testing.T) {
			got, _ := httpFromGRPC(status.Error(tt.grpcCode, "сообщение"))
			if got != tt.wantHTTP {
				t.Errorf("код %s сопоставлен с HTTP %d, ожидался %d", tt.grpcCode, got, tt.wantHTTP)
			}
		})
	}

	// Не-gRPC ошибка не должна приводить к панику.
	if got, _ := httpFromGRPC(errors.New("обычная ошибка")); got != http.StatusInternalServerError {
		t.Errorf("не-gRPC ошибка сопоставлена с HTTP %d, ожидался 500", got)
	}
}

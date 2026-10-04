package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	apitokens "github.com/nikaydo/grpc-contract/gen/apiToken"
	"github.com/nikaydo/grpc-contract/gen/video"
)

// createTokenRequest — тело запроса на выпуск API-токена.
type createTokenRequest struct {
	Name string `json:"name"`
}

// deleteTokenRequest — тело запроса на отзыв API-токена.
type deleteTokenRequest struct {
	Token string `json:"token"`
}

// tokenResponse — ответ на выпуск токена.
//
// Значение токена возвращается один раз: сервис хранит только хеш и отдать
// его повторно не может.
type tokenResponse struct {
	Token string `json:"token"`
}

// CreateToken выпускает API-токен для текущего пользователя.
func (h *Handlers) CreateToken(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	s, ok := sessionFrom(ctx)
	if !ok {
		fail(ctx, w, h.Log, http.StatusUnauthorized, "no_session", "требуется авторизация")
		return
	}

	// Тело необязательно: имя токена пока не используется, но принимается,
	// чтобы форма могла его отправлять.
	var body createTokenRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &body, h.Cfg.MaxRequestBytes); err != nil {
			failErr(ctx, w, h.Log, http.StatusBadRequest, "bad_request", "не удалось прочитать тело запроса", err)
			return
		}
	}

	resp, err := h.Tokens.Create(ctx, &apitokens.CreateRequest{UserId: s.UserID})
	if err != nil {
		forwardError(ctx, w, h.Log, err)
		return
	}

	writeJSON(w, http.StatusCreated, tokenResponse{Token: resp.GetToken()})
}

// ListTokens возвращает хеши токенов пользователя.
//
// Значения токенов не возвращаются: сервис их не хранит. Пользователю
// доступен только созданный при выпуске токен.
func (h *Handlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	s, ok := sessionFrom(ctx)
	if !ok {
		fail(ctx, w, h.Log, http.StatusUnauthorized, "no_session", "требуется авторизация")
		return
	}

	resp, err := h.Tokens.Get(ctx, &apitokens.GetRequest{UserId: s.UserID})
	if err != nil {
		forwardError(ctx, w, h.Log, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"hashes": resp.GetTokens().GetTokens()})
}

// DeleteToken отзывает API-токен.
//
// Идентификатор владельца берётся из сессии, а не из тела запроса: иначе можно
// было бы отозвать чужой токен.
func (h *Handlers) DeleteToken(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	s, ok := sessionFrom(ctx)
	if !ok {
		fail(ctx, w, h.Log, http.StatusUnauthorized, "no_session", "требуется авторизация")
		return
	}

	var body deleteTokenRequest
	if err := decodeJSON(w, r, &body, h.Cfg.MaxRequestBytes); err != nil {
		failErr(ctx, w, h.Log, http.StatusBadRequest, "bad_request", "не удалось прочитать тело запроса", err)
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		fail(ctx, w, h.Log, http.StatusBadRequest, "empty_token", "укажите токен для отзыва")
		return
	}

	if _, err := h.Tokens.Delete(ctx, &apitokens.DeleteRequest{
		UserId: s.UserID,
		Token:  body.Token,
	}); err != nil {
		forwardError(ctx, w, h.Log, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// uploadRequestMultipart — приём загрузки видео через multipart.
func (h *Handlers) UploadVideo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	s, ok := sessionFrom(ctx)
	if !ok {
		fail(ctx, w, h.Log, http.StatusUnauthorized, "no_session", "требуется авторизация")
		return
	}

	// Ограничение на тело запроса обязательно: без него один запрос мог
	// загрузить в память сколько угодно данных.
	r.Body = http.MaxBytesReader(w, r.Body, h.Cfg.MaxUploadBytes)

	// Файл читается в память, потому что контракт передаёт видео целиком.
	file, header, err := r.FormFile("video")
	if err != nil {
		failErr(ctx, w, h.Log, http.StatusBadRequest, "no_file", "файл не передан", err)
		return
	}
	defer func() { _ = file.Close() }()

	if header.Size > h.Cfg.MaxUploadBytes {
		fail(ctx, w, h.Log, http.StatusRequestEntityTooLarge, "file_too_large",
			fmt.Sprintf("файл больше %d байт", h.Cfg.MaxUploadBytes))
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, h.Cfg.MaxUploadBytes+1))
	if err != nil {
		failErr(ctx, w, h.Log, http.StatusBadRequest, "read_failed", "не удалось прочитать файл", err)
		return
	}
	if int64(len(data)) > h.Cfg.MaxUploadBytes {
		fail(ctx, w, h.Log, http.StatusRequestEntityTooLarge, "file_too_large",
			fmt.Sprintf("файл больше %d байт", h.Cfg.MaxUploadBytes))
		return
	}

	name := r.FormValue("name")
	if strings.TrimSpace(name) == "" {
		name = header.Filename
	}

	if _, err := h.Video.Add(ctx, &video.AddRequest{
		UserId: s.UserID,
		Video:  data,
		Name:   name,
	}); err != nil {
		forwardError(ctx, w, h.Log, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// ListVideos возвращает видео пользователя по названию.
func (h *Handlers) ListVideos(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	s, ok := sessionFrom(ctx)
	if !ok {
		fail(ctx, w, h.Log, http.StatusUnauthorized, "no_session", "требуется авторизация")
		return
	}

	// Название передаётся параметром запроса: это не секрет, и в отличие от
	// токена его попадание в журнал не опасно.
	name := r.URL.Query().Get("name")

	resp, err := h.Video.Get(ctx, &video.GetRequest{
		UserId:    s.UserID,
		VideoName: name,
	})
	if err != nil {
		forwardError(ctx, w, h.Log, err)
		return
	}

	videos := resp.GetVideo().GetVideo()
	out := make([]map[string]string, 0, len(videos))
	for _, v := range videos {
		out = append(out, map[string]string{
			"uuid":  v.GetUuid(),
			"title": v.GetTitle(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"videos": out})
}

// DeleteVideo удаляет видео пользователя.
func (h *Handlers) DeleteVideo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	s, ok := sessionFrom(ctx)
	if !ok {
		fail(ctx, w, h.Log, http.StatusUnauthorized, "no_session", "требуется авторизация")
		return
	}

	videoID := r.PathValue("videoID")
	if videoID == "" {
		fail(ctx, w, h.Log, http.StatusBadRequest, "missing_id", "укажите идентификатор видео")
		return
	}

	if _, err := h.Video.Delete(ctx, &video.DeleteRequest{
		UserId: s.UserID,
		Uuid:   videoID,
	}); err != nil {
		forwardError(ctx, w, h.Log, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// StreamVideo отдаёт видео клиенту.
//
// Файл не загружается в память целиком: поток gRPC пробрасывается в HTTP
// по мере поступления сообщений.
func (h *Handlers) StreamVideo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.callCtx(r)
	defer cancel()

	if _, ok := sessionFrom(ctx); !ok {
		fail(ctx, w, h.Log, http.StatusUnauthorized, "no_session", "требуется авторизация")
		return
	}

	videoID := r.PathValue("videoID")
	if videoID == "" {
		fail(ctx, w, h.Log, http.StatusBadRequest, "missing_id", "укажите идентификатор видео")
		return
	}

	stream, err := h.Video.Stream(ctx, &video.StreamRequest{Uuid: videoID})
	if err != nil {
		forwardError(ctx, w, h.Log, err)
		return
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.WriteHeader(http.StatusOK)

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			// Заголовки уже отправлены, статус изменить нельзя: остаётся
			// прервать запись и записать причину в лог.
			h.Log.Error("передача видео прервана", "video_id", videoID, "err", err)
			return
		}
		if _, err := w.Write(msg.GetVideo()); err != nil {
			h.Log.Debug("клиент прервал загрузку", "video_id", videoID, "err", err)
			return
		}
	}
}

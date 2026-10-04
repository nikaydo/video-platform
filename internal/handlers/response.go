package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// problem — единый формат ошибки.
type problem struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// writeJSON пишет ответ в JSON.
func writeJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Заголовки уже отправлены, статус изменить нельзя.
		slog.Default().Error("не удалось записать ответ", "err", err)
	}
}

// writeError пишет безопасную ошибку, а подробности уходит в лог.
func writeError(ctx context.Context, w http.ResponseWriter, log *slog.Logger, statusCode int, code, public string, cause error) {
	if log == nil {
		log = slog.Default()
	}
	if cause != nil {
		log.LogAttrs(ctx, levelFor(statusCode), "запрос не выполнен",
			slog.String("code", code),
			slog.String("public", public),
			slog.String("cause", cause.Error()),
		)
	}
	writeJSON(w, statusCode, problem{Error: public, Code: code})
}

func levelFor(statusCode int) slog.Level {
	switch {
	case statusCode >= 500:
		return slog.LevelError
	case statusCode == http.StatusTooManyRequests,
		statusCode == http.StatusUnauthorized,
		statusCode == http.StatusForbidden:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func fail(ctx context.Context, w http.ResponseWriter, log *slog.Logger, statusCode int, code, public string) {
	writeError(ctx, w, log, statusCode, code, public, nil)
}

func failErr(ctx context.Context, w http.ResponseWriter, log *slog.Logger, statusCode int, code, public string, cause error) {
	writeError(ctx, w, log, statusCode, code, public, cause)
}

// decodeJSON разбирает тело запроса с ограничением размера.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return fmt.Errorf("тело запроса превышает %d байт", maxBytes)
		}
		return fmt.Errorf("не удалось разобрать JSON: %w", err)
	}
	return nil
}

// httpFromGRPC переводит код gRPC в HTTP-код.
//
// Сервисы возвращают ошибки в виде gRPC-статусов. Без перевода клиент
// получал бы 500 на любую ошибку, включая «не авторизован» или «не найдено».
func httpFromGRPC(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}
	st, ok := status.FromError(err)
	if !ok {
		return http.StatusInternalServerError, "внутренняя ошибка сервера"
	}
	switch st.Code() {
	case codes.OK:
		return http.StatusOK, ""
	case codes.InvalidArgument:
		return http.StatusBadRequest, st.Message()
	case codes.Unauthenticated:
		return http.StatusUnauthorized, st.Message()
	case codes.PermissionDenied:
		return http.StatusForbidden, st.Message()
	case codes.NotFound:
		return http.StatusNotFound, st.Message()
	case codes.AlreadyExists:
		return http.StatusConflict, st.Message()
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests, st.Message()
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout, "сервис не ответил вовремя"
	case codes.Unavailable:
		return http.StatusServiceUnavailable, "сервис временно недоступен"
	case codes.Internal, codes.Unknown, codes.DataLoss:
		// Текст внутренней ошибки наружу не отдаётся: он может содержать
		// детали реализации.
		return http.StatusInternalServerError, "внутренняя ошибка сервера"
	default:
		return http.StatusInternalServerError, "внутренняя ошибка сервера"
	}
}

// grpcCodeName возвращает машинное имя кода ошибки.
func grpcCodeName(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return "internal"
	}
	return strings.ToLower(st.Code().String())
}

// clientMessage возвращает текст ошибки, безопасный для показа клиенту.
//
// Для кодов, означающих нарушение доступа или неверный ввод, сообщение
// сервиса полезно пользователю. Для внутренних ошибок — нет: там могут быть
// детали реализации.
func clientMessage(err error) string {
	code, msg := httpFromGRPC(err)
	if code >= 500 {
		return "не удалось выполнить операцию"
	}
	return msg
}

// forwardError отвечает клиенту по ошибке вызова gRPC-сервиса.
func forwardError(ctx context.Context, w http.ResponseWriter, log *slog.Logger, err error) {
	code := http.StatusInternalServerError
	if c, _ := httpFromGRPC(err); c >= 400 && c < 600 {
		code = c
	}
	writeError(ctx, w, log, code, grpcCodeName(err), clientMessage(err), err)
}

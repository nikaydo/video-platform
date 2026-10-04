// Package server запускает HTTP-сервер с корректным завершением работы.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Config — параметры HTTP-сервера.
type Config struct {
	Addr string
	// ReadHeaderTimeout защищает от медленных заголовков: без него клиент
	// может держать соединение, ничего не отправив.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	// WriteTimeout больше обычного: потоковая передача видео идёт долго.
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	MaxHeaderBytes  int
	ShutdownTimeout time.Duration
}

// NewConfig возвращает настройки таймаутов по умолчанию.
func NewConfig(addr string, shutdown time.Duration) Config {
	if shutdown <= 0 {
		shutdown = 30 * time.Second
	}
	return Config{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Запись ограничена отдельным таймаутом: отдача видео занимает время,
		// но и не должна длиться бесконечно.
		WriteTimeout:    10 * time.Minute,
		IdleTimeout:     120 * time.Second,
		MaxHeaderBytes:  1 << 20,
		ShutdownTimeout: shutdown,
	}
}

// New собирает http.Server.
func New(cfg Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
}

// Run запускает сервер и останавливает его по сигналу.
func Run(ctx context.Context, srv *http.Server, cfg Config, log *slog.Logger) error {
	errCh := make(chan error, 1)

	go func() {
		log.Info("сервер запущен", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("получен сигнал завершения")
	}

	// Контекст приложения к этому моменту отменён, поэтому для ожидания
	// завершения запросов создаётся отдельный.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	log.Info("сервер остановлен")
	return <-errCh
}

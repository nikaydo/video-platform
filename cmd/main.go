// Command video-platform — HTTP-шлюз системы видеохостинга.
//
// Шлюз принимает запросы браузера и машинных клиентов, проверяет сессию и
// API-токен, затем обращается к сервисам по gRPC.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nikaydo/video-platform/internal/clients"
	"github.com/nikaydo/video-platform/internal/config"
	"github.com/nikaydo/video-platform/internal/handlers"
	"github.com/nikaydo/video-platform/internal/router"
	"github.com/nikaydo/video-platform/internal/server"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("шлюз завершился с ошибкой", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log.Info("конфигурация загружена", "addr", cfg.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Подключения к сервисам создаются лениво: шлюз поднимается, даже если
	// зависимости ещё не готовы.
	grpcClients, err := clients.Dial(cfg.AuthAddr(), cfg.TokenAddr(), cfg.VideoAddr(), cfg.GRPCTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := grpcClients.Close(); err != nil {
			log.Error("не удалось закрыть соединения с сервисами", "err", err)
		}
	}()

	log.Info("подключения к сервисам настроены",
		"auth", cfg.AuthAddr(),
		"tokens", cfg.TokenAddr(),
		"video", cfg.VideoAddr(),
	)

	h := handlers.New(grpcClients.Auth, grpcClients.Tokens, grpcClients.Video, cfg, log)

	// Заголовки безопасности включаются только вместе с Secure-cookie:
	// иначе приложение, доступное по http, получит cookie, которые браузер
	// не отправит, и защита окажется нерабочей.
	handler := router.New(router.Params{
		Handlers:      h,
		StaticDir:     cfg.StaticDir,
		SecureHeaders: cfg.CookieSecure,
	})

	srvCfg := server.NewConfig(cfg.Addr(), cfg.ShutdownTimeout)
	return server.Run(ctx, server.New(srvCfg, handler), srvCfg, log)
}

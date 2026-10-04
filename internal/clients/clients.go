// Package clients оборачивает gRPC-клиентов сервисов.
//
// Обёртки нужны по трём причинам:
//
//   - добавляют таймаут ко всем��апыкам: без него запрос к недоступному
//     сервису висел бы до конца соединения;
//   - подставляют контекст запроса, чтобы отмена HTTP-запроса прерывала и
//     вызов gRPC;
//   - переводят ошибки gRPC в понятные HTTP-коды в одном месте.
package clients

import (
	"fmt"
	"time"

	apiTokens "github.com/nikaydo/grpc-contract/gen/apiToken"
	"github.com/nikaydo/grpc-contract/gen/auth"
	"github.com/nikaydo/grpc-contract/gen/video"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Clients содержит подключения к сервисам.
type Clients struct {
	Auth   auth.AuthClient
	Tokens apiTokens.ApiTokenClient
	Video  video.VideoClient

	conns []*grpc.ClientConn
}

// Dial подключается ко всем сервисам.
//
// Соединения создаются лениво: сервис может ещё не подняться, и падать при
// старте из-за этого не нужно — вызов вернёт ошибку, когда придёт время.
func Dial(authAddr, tokenAddr, videoAddr string, timeout time.Duration) (*Clients, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	authConn, err := dial(authAddr, timeout, 4<<20, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("не удалось подключиться к сервису авторизации: %w", err)
	}

	tokenConn, err := dial(tokenAddr, timeout, 4<<20, 4<<20)
	if err != nil {
		_ = authConn.Close()
		return nil, fmt.Errorf("не удалось подключиться к сервису токенов: %w", err)
	}

	// Видео передаётся целиком при загрузке и порциями при отправке,
	// поэтому предел сообщения здесь заметно больше, чем у остальных.
	videoConn, err := dial(videoAddr, timeout, 25<<20, 25<<20)
	if err != nil {
		_ = authConn.Close()
		_ = tokenConn.Close()
		return nil, fmt.Errorf("не удалось подключиться к сервису видео: %w", err)
	}

	return &Clients{
		Auth:   auth.NewAuthClient(authConn),
		Tokens: apiTokens.NewApiTokenClient(tokenConn),
		Video:  video.NewVideoClient(videoConn),
		conns:  []*grpc.ClientConn{authConn, tokenConn, videoConn},
	}, nil
}

// dial создаёт одно соединение с общими настройками.
func dial(addr string, timeout time.Duration, maxSend, maxRecv int) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr,
		// Внутренняя сеть без TLS. Наружу (в сторону браузера) соединение
		// защищается HTTP-сервером, а не этим каналом.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(maxSend),
			grpc.MaxCallRecvMsgSize(maxRecv),
			// Таймаут на каждый вызов: без него запрос к зависшему сервису
			// держал бы соединение до бесконечности.
			grpc.WaitForReady(false),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
}

// Close закрывает все соединения.
func (c *Clients) Close() error {
	var firstErr error
	for _, conn := range c.conns {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

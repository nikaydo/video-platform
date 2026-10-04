package handlers

import (
	"io"
	"log/slog"
)

// slogDiscard возвращает логгер, отбрасывающий записи: тесты не должны
// засорять вывод сообщениями о неудачных запросах.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

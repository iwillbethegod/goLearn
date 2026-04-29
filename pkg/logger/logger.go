package logger

import (
	"log/slog"
	"os"
)

func NewLogger() *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{AddSource: false})
	return slog.New(handler)
}

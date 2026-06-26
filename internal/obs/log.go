package obs

import (
	"bytes"
	"log/slog"
	"os"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

type Config struct {
	Format string
	Level  string
}

func NewLogger(cfg config.Obs) *slog.Logger {
	var handler slog.Handler

	opts := slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}

	handler = slog.NewTextHandler(os.Stdout, &opts)
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stdout, &opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)

	return logger
}

func NewLoggerWithWriter(cfg Config, buf *bytes.Buffer) *slog.Logger {
	var handler slog.Handler

	opts := slog.HandlerOptions{
		Level: parseLevel(cfg.Level),
	}

	handler = slog.NewTextHandler(buf, &opts)
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(buf, &opts)
	}

	return slog.New(handler)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

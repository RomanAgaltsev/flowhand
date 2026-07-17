package obs

import (
	"bytes"
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

// traceHandler stamps trace_id/span_id onto records that carry a sampled span,
// which is what lets Grafana jump from a Tempo span to the matching Loki lines.
// Only the *Context log methods can see the span - slog.Info and friends cannot.
type traceHandler struct {
	slog.Handler
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs and WithGroup must re-wrap: the embedded handler would otherwise
// return a bare handler and silently drop the trace_id stamping.
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return traceHandler{Handler: h.Handler.WithGroup(name)}
}

type Config struct {
	Format string
	Level  string
}

func NewLogger(cfg *config.Obs) *slog.Logger {
	var handler slog.Handler

	opts := slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}

	handler = slog.NewTextHandler(os.Stdout, &opts)
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stdout, &opts)
	}

	logger := slog.New(traceHandler{Handler: handler})

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

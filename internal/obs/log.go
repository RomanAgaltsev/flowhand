package obs

import (
	"context"
	"io"
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

func newLogger(format, level string, w io.Writer) *slog.Logger {
	opts := slog.HandlerOptions{Level: parseLevel(level)}
	var h slog.Handler = slog.NewTextHandler(w, &opts)
	if format == "json" {
		h = slog.NewJSONHandler(w, &opts)
	}
	// Wrapped here, once: every caller — production and test — gets the same
	// handler chain, so a test can actually observe the trace stamping.
	return slog.New(traceHandler{Handler: h})
}

// NewLogger builds the process logger from observability config, stamping
// trace/span IDs onto records emitted with the *Context methods.
func NewLogger(cfg *config.Obs) *slog.Logger {
	return newLogger(cfg.LogFormat, cfg.LogLevel, os.Stdout)
}

// NewLoggerWithWriter builds a logger that writes to buf, for use in tests.
func NewLoggerWithWriter(cfg *config.Obs, w io.Writer) *slog.Logger {
	return newLogger(cfg.LogFormat, cfg.LogLevel, w)
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

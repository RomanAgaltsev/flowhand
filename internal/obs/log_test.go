package obs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/obs"
)

func TestNewLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	lg := obs.NewLoggerWithWriter(&config.Obs{LogFormat: "json", LogLevel: "info"}, &buf)
	lg.Info("hello", "user", "alice")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	assert.Equal(t, "hello", m["msg"])
	assert.Equal(t, "alice", m["user"])
}

func TestNewLogger_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	lg := obs.NewLoggerWithWriter(&config.Obs{LogFormat: "json", LogLevel: "warn"}, &buf)
	lg.Info("suppressed")
	assert.Empty(t, buf.String())
	lg.Warn("emitted")
	assert.Contains(t, buf.String(), "emitted")
}

func TestNewLogger_StampsTraceIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	lg := obs.NewLoggerWithWriter(&config.Obs{LogFormat: "json", LogLevel: "info"}, &buf)

	tp := sdktrace.NewTracerProvider() // local, never global — avoids the racy SetTracerProvider
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	lg.InfoContext(ctx, "hello")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	assert.Equal(t, span.SpanContext().TraceID().String(), m["trace_id"])
	assert.Equal(t, span.SpanContext().SpanID().String(), m["span_id"])
}

func TestNewLogger_TraceStampingSurvivesWithAttrs(t *testing.T) {
	// The re-wrapping bug this guards against is silent: With() returns a bare
	// handler and trace_id quietly stops appearing on every derived logger.
	var buf bytes.Buffer
	lg := obs.NewLoggerWithWriter(&config.Obs{LogFormat: "json", LogLevel: "info"}, &buf).
		With("component", "api").WithGroup("req")

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	lg.InfoContext(ctx, "hello")

	assert.Contains(t, buf.String(), span.SpanContext().TraceID().String())
}

func TestNewLogger_NoTraceOutsideSpan(t *testing.T) {
	var buf bytes.Buffer
	lg := obs.NewLoggerWithWriter(&config.Obs{LogFormat: "json", LogLevel: "info"}, &buf)
	lg.InfoContext(context.Background(), "hello")
	assert.NotContains(t, buf.String(), "trace_id")
}

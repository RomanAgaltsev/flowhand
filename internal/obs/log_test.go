package obs_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/obs"
)

func TestNewLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	lg := obs.NewLoggerWithWriter(obs.Config{Format: "json", Level: "info"}, &buf)
	lg.Info("hello", "user", "alice")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	assert.Equal(t, "hello", m["msg"])
	assert.Equal(t, "alice", m["user"])
}

func TestNewLogger_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	lg := obs.NewLoggerWithWriter(obs.Config{Format: "json", Level: "warn"}, &buf)
	lg.Info("suppressed")
	assert.Empty(t, buf.String())
	lg.Warn("emitted")
	assert.Contains(t, buf.String(), "emitted")
}

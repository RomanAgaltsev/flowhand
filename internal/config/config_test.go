package config_test

import (
	"os"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

func TestLoad_DefaultsOnly(t *testing.T) {
	cfg, err := config.Load("", nil)
	require.NoError(t, err)
	assert.Equal(t, "dev", cfg.Env)
	assert.Equal(t, ":8080", cfg.HTTP.Addr)
}

func TestLoad_EnvOverridesDefault(t *testing.T) {
	t.Setenv("FLOWHAND_HTTP_ADDR", ":9999")
	cfg, err := config.Load("", nil)
	require.NoError(t, err)
	assert.Equal(t, ":9999", cfg.HTTP.Addr)
}

func TestLoad_FileAndEnv_EnvWins(t *testing.T) {
	tmp := t.TempDir() + "/cfg.yaml"
	require.NoError(t, os.WriteFile(tmp, []byte("http:\n  addr: :7777\n"), 0o600))
	t.Setenv("FLOWHAND_HTTP_ADDR", ":8888")
	cfg, err := config.Load(tmp, nil)
	require.NoError(t, err)
	assert.Equal(t, ":8888", cfg.HTTP.Addr)
}

func TestLoad_FlagOverridesEnv(t *testing.T) {
	t.Setenv("FLOWHAND_OBS_LOG_LEVEL", "warn")
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("log-level", "info", "")
	require.NoError(t, fs.Parse([]string{"--log-level=debug"}))

	cfg, err := config.Load("", fs)
	require.NoError(t, err)
	assert.Equal(t, "debug", cfg.Obs.LogLevel)
}

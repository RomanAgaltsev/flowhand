package config_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

func TestLoad_DefaultsOnly(t *testing.T) {
	cfg, err := config.Load("cfg.yaml", nil)
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
	os.WriteFile(tmp, []byte("http:\n  addr: :7777\n"), 0600)
	t.Setenv("FLOWHAND_HTTP_ADDR", ":8888")
	cfg, err := config.Load(tmp, nil)
	require.NoError(t, err)
	assert.Equal(t, ":8888", cfg.HTTP.Addr)
}

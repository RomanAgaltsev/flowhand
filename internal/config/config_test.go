package config_test

import (
	"os"
	"reflect"
	"testing"
	"time"

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

// The cache's timeouts have to be real durations by default: go-redis reads zero
// as "use my own default", which is seconds, and a koanf key that does not decode
// would leave exactly that.
func TestLoad_RedisTimeoutDefaults(t *testing.T) {
	cfg, err := config.Load("", nil)
	require.NoError(t, err)
	assert.Equal(t, 250*time.Millisecond, cfg.Redis.DialTimeout)
	assert.Equal(t, 100*time.Millisecond, cfg.Redis.ReadTimeout)
	assert.Equal(t, 100*time.Millisecond, cfg.Redis.WriteTimeout)
	assert.Equal(t, 100*time.Millisecond, cfg.Redis.PoolTimeout)
}

func TestLoad_RedisTimeoutFromEnv(t *testing.T) {
	t.Setenv("FLOWHAND_REDIS_READ_TIMEOUT", "2s")
	cfg, err := config.Load("", nil)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, cfg.Redis.ReadTimeout)
}

// TestLoad_EverySectionIsMaterialized fails when a section gets defaults and a
// struct but no line in Load's return. That is not a compile error: Redis and Cache
// were silently zero, so the task cache refused to build and the server would not
// start, while cfg.Redis.Addr fell back to go-redis's own default unnoticed.
func TestLoad_EverySectionIsMaterialized(t *testing.T) {
	t.Setenv("FLOWHAND_DB_DSN", "postgres://localhost/flowhand")

	cfg, err := config.Load("", nil)
	require.NoError(t, err)

	v := reflect.ValueOf(*cfg)
	for i := range v.NumField() {
		assert.False(t, v.Field(i).IsZero(), "config.%s is empty: Load never fills it in", v.Type().Field(i).Name)
	}
	assert.Equal(t, 10000, cfg.Cache.Capacity)
	assert.Equal(t, 5*time.Minute, cfg.Cache.TTL)
	assert.Equal(t, "localhost:6379", cfg.Redis.Addr)
}

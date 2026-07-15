package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

type Config struct {
	Env     string `koanf:"env"` // dev, staging, prod
	Version string `koanf:"version"`
	HTTP    HTTP   `koanf:"http"`
	DB      DB     `koanf:"db"`
	Obs     Obs    `koanf:"obs"`
}

type HTTP struct {
	Addr            string        `koanf:"addr"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
}

type DB struct {
	DSN             string        `koanf:"dsn"`
	MaxConns        int32         `koanf:"max_conns"`
	MinConns        int32         `koanf:"min_conns"`
	MaxConnLifetime time.Duration `koanf:"max_conn_lifetime"`
}

type Obs struct {
	LogLevel     string `koanf:"log_level"`
	LogFormat    string `koanf:"log_format"`    // json or text
	OTLPEndpoint string `koanf:"otlp_endpoint"` // host:port, gRPC
	ServiceName  string `koanf:"service_name"`
}

func Load(path string, flags *pflag.FlagSet) (*Config, error) {
	k := koanf.New(".")

	// 1. Defaults (lowest precedence).
	err := k.Load(confmap.Provider(map[string]any{
		"env":                   "dev",
		"version":               "dev",
		"http.addr":             ":8080",
		"http.shutdown_timeout": "10s",
		"db.dsn":                "",
		"db.max_conns":          10,
		"db.min_conns":          2,
		"db.max_conn_lifetime":  0,
		"obs.log_level":         "info",
		"obs.log_format":        "json",
		"obs.otlp_endpoint":     "localhost:4317",
		"obs.service_name":      "flowhand",
	}, "."), nil)
	if err != nil {
		return nil, err
	}

	// 2. Config file (only when a path is given).
	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("load config file %q: %w", path, err)
		}
	}

	// 3. Environment: FLOWHAND_HTTP_ADDR -> http.addr
	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: "FLOWHAND_",
		TransformFunc: func(key, value string) (string, any) {
			key = strings.TrimPrefix(key, "FLOWHAND_") // HTTP_ADDR
			key = strings.ToLower(key)                 // http_addr
			key = strings.ReplaceAll(key, "_", ".")    // http.addr
			return key, value
		},
	}), nil); err != nil {
		return nil, err
	}

	// 4. Flags (highest precedence), supplied by cobra in Task 8.
	if flags != nil {
		if err := k.Load(posflag.ProviderWithValue(flags, ".", k,
			func(key, value string) (string, any) {
				m := map[string]string{
					"env":               "env",
					"version":           "version",
					"addr":              "http.addr",
					"shutdown_timeout":  "http.shutdown_timeout",
					"dsn":               "db.dsn",
					"max_conns":         "db.max_conns",
					"min_conns":         "db.min_conns",
					"max_conn_lifetime": "db.max_conn_lifetime",
					"log_level":         "obs.log_level",
					"log_format":        "obs.log_format",
					"otlp_endpoint":     "obs.otlp_endpoint",
					"service_name":      "obs.service_name",
				}
				if dst, ok := m[key]; ok {
					return dst, value
				}
				return "", value // ignore unknown flags
			},
		), nil); err != nil {
			return nil, err
		}
	}

	// 5. Materialize.
	return &Config{
		Env:     k.String("env"),
		Version: k.String("version"),
		HTTP: HTTP{
			Addr:            k.String("http.addr"),
			ShutdownTimeout: k.Duration("http.shutdown_timeout"),
		},
		DB: DB{
			DSN:             k.String("db.dsn"),
			MaxConns:        int32(k.Int("db.max_conns")),
			MinConns:        int32(k.Int("db.min_conns")),
			MaxConnLifetime: k.Duration("db.max_conn_lifetime"),
		},
		Obs: Obs{
			LogLevel:     k.String("obs.log_level"),
			LogFormat:    k.String("obs.log_format"),
			OTLPEndpoint: k.String("obs.otlp_endpoint"),
			ServiceName:  k.String("obs.service_name"),
		},
	}, nil
}

// Command flowhand-demo is the reference producer: it submits one task per
// interval to a running flowhand server and exposes its own /metrics so the
// dev stack's Prometheus can chart submit throughput.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var submits = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "flowhand_demo_submits_total",
		Help: "Task submissions attempted by the reference producer.",
	},
	[]string{"result"}, // ok | error
)

func main() {
	if err := run(); err != nil {
		slog.Error("flowhand-demo", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	target := envOr("FLOWHAND_URL", "http://host.docker.internal:8080")
	interval := envDur("DEMO_INTERVAL", time.Second)
	metricsAddr := envOr("DEMO_METRICS_ADDR", ":8081")

	endpoint, err := url.JoinPath(target, "/v1/tasks")
	if err != nil {
		return fmt.Errorf("bad FLOWHAND_URL %q: %w", target, err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{
		Addr:              metricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("metrics listening", "addr", metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server", "err", err)
		}
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	slog.Info("submitting", "endpoint", endpoint, "interval", interval)

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tick.C:
			if err := submitOne(ctx, client, endpoint); err != nil {
				submits.WithLabelValues("error").Inc()
				slog.Warn("submit failed", "err", err)
				continue
			}
			submits.WithLabelValues("ok").Inc()
		}
	}

	// Fresh context: the one above is already canceled by the signal.
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := metricsSrv.Shutdown(sctx); err != nil {
		return fmt.Errorf("metrics shutdown: %w", err)
	}
	slog.Info("stopped")
	return nil
}

func envOr(env string, def string) string {
	e := os.Getenv(env)
	if e == "" {
		return def
	}
	return e
}

func envDur(env string, def time.Duration) time.Duration {
	ds := os.Getenv(env)
	if ds == "" {
		return def
	}
	d, err := time.ParseDuration(ds)
	if err != nil {
		return def
	}
	return d
}

// createTaskRequest mirrors CreateTaskRequest in api/openapi.yaml.
type createTaskRequest struct {
	Handler        string         `json:"handler"`
	Payload        map[string]any `json:"payload,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

func submitOne(ctx context.Context, client *http.Client, endpoint string) error {
	body, err := json.Marshal(createTaskRequest{
		Handler:        "echo",
		Payload:        map[string]any{"msg": "hello", "n": 42},
		IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post task: %w", err)
	}
	defer func() {
		// Drain before close so the connection returns to the idle pool.
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			slog.Debug("drain response body", "err", err)
		}
		if err := resp.Body.Close(); err != nil {
			slog.Debug("close response body", "err", err)
		}
	}()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}

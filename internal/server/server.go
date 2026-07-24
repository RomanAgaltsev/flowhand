package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/RomanAgaltsev/flowhand/internal/api"
	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	"github.com/RomanAgaltsev/flowhand/internal/config"
)

// Run serves the API, metrics and pprof endpoints until ctx is cancelled, then
// gracefully shuts the server down.
func Run(ctx context.Context, cfg *config.Config, h *api.Handler, reg *prometheus.Registry) error {
	oasSrv, err := oas.NewServer(h, oas.WithErrorHandler(api.ErrorHandler))
	if err != nil {
		return fmt.Errorf("new oas server: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", otelhttp.NewHandler(oasSrv, "flowhand.http",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	))
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.InfoContext(ctx, "listening", "addr", cfg.HTTP.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout) //nolint:contextcheck // deliberate detached shutdown context (see M1)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil { //nolint:contextcheck // deliberate detached shutdown context
			return err
		}
		return <-errCh // ListenAndServe has already returned (ErrServerClosed → nil); no goroutine left live
	}
}

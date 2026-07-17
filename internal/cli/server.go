package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RomanAgaltsev/flowhand/internal/api"
	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/obs"
	"github.com/RomanAgaltsev/flowhand/internal/server"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the flowhand control plane",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(mustString(cmd, "config"), cmd.Flags())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		shutdown, reg, err := obs.Init(ctx, cfg)
		if err != nil {
			return fmt.Errorf("init obs: %w", err)
		}
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(sctx)
		}()

		pool, err := storage.NewPool(ctx, cfg.DB)
		if err != nil {
			return fmt.Errorf("pool: %w", err)
		}
		defer pool.Close()

		q := queries.New(pool)
		h := api.NewHandler(q, slog.Default())

		return server.Run(ctx, cfg, h, reg)

	},
}

func mustString(cmd *cobra.Command, name string) string {
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		panic(fmt.Sprintf("flag %q: %v", name, err))
	}
	return v
}

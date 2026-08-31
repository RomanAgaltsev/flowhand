package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/flowhand/internal/api"
	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/obs"
	"github.com/RomanAgaltsev/flowhand/internal/repository/outbox"
	repotasks "github.com/RomanAgaltsev/flowhand/internal/repository/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/server"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
	"github.com/RomanAgaltsev/flowhand/internal/storage/txmgr"
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
			if err := shutdown(sctx); err != nil {
				slog.Error("obs shutdown failed", "err", err)
			}
		}()

		pool, err := storage.NewPool(ctx, cfg.DB)
		if err != nil {
			return fmt.Errorf("pool: %w", err)
		}
		defer pool.Close()

		// Bottom-up: pool -> tx manager (Resolver+TxRunner) -> repos -> service -> handler.
		txm := txmgr.New(pool)
		tasksRepo := repotasks.New(txm)
		outboxRepo := outbox.New()

		h := api.NewHandler(
			task.NewCommander(tasksRepo, outboxRepo, txm, acceptAnyHandler{}),
			task.NewQuerier(tasksRepo),
			slog.Default(),
		)

		return server.Run(ctx, cfg, h, reg)
	},
}

// acceptAnyHandler is a placeholder tasks.HandlerCatalog: it accepts every
// non-empty name, which is exactly the validation the control plane did before
// D2 gave Task a real Handler value object.
//
// It is deliberately permissive rather than empty. An empty catalog would
// reject every submission, and shipping a catalog that silently rejects real
// traffic is worse than shipping one that admits it has no opinion yet. The
// worker runtime (CE1) owns the real registry; this type goes away with it.
//
// NewHandler already rejects the empty name before consulting a catalog, so
// Has never sees one.
type acceptAnyHandler struct{}

func (acceptAnyHandler) Has(name string) bool { return name != "" }

func mustString(cmd *cobra.Command, name string) string {
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		panic(fmt.Sprintf("flag %q: %v", name, err))
	}
	return v
}

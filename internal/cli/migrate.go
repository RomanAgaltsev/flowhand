package cli

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply or inspect database migrations under a session lock",
}

var migrateUpCmd = &cobra.Command{
	Use:   "up",
	Short: "apply all pending migrations",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return withMigrateProvider(cmd, func(ctx context.Context, p *goose.Provider) error {
			results, err := p.Up(ctx)
			if err != nil {
				return fmt.Errorf("migrate up: %w", err)
			}
			if len(results) == 0 {
				cmd.Println("database is up to date, nothing to apply")
				return nil
			}
			for _, r := range results {
				cmd.Printf("applied %s (%s)\n", r.Source.Path, r.Duration.Round(time.Millisecond))
			}
			return nil
		})
	},
}

var migrateDownCmd = &cobra.Command{
	Use:   "down",
	Short: "roll back the most recently applied migration",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return withMigrateProvider(cmd, func(ctx context.Context, p *goose.Provider) error {
			res, err := p.Down(ctx)
			if errors.Is(err, goose.ErrNoNextVersion) {
				cmd.Println("nothing to roll back")
				return nil
			}
			if err != nil {
				return fmt.Errorf("migrate down: %w", err)
			}
			cmd.Printf("rolled back %s\n", res.Source.Path)
			return nil
		})
	},
}

var migrateStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "list migrations and their applied state",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return withMigrateProvider(cmd, func(ctx context.Context, p *goose.Provider) error {
			statuses, err := p.Status(ctx)
			if err != nil {
				return fmt.Errorf("migrate status: %w", err)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 3, ' ', 0)
			if _, err := fmt.Fprintln(w, "VERSION\tSTATE\tAPPLIED AT\tSOURCE"); err != nil {
				return fmt.Errorf("write header: %w", err)
			}
			for _, s := range statuses {
				applied := "-"
				if !s.AppliedAt.IsZero() {
					applied = s.AppliedAt.Format(time.RFC3339)
				}
				if _, err := fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
					s.Source.Version, s.State, applied, s.Source.Path); err != nil {
					return fmt.Errorf("write row: %w", err)
				}
			}
			return w.Flush()
		})
	},
}

func init() {
	migrateCmd.AddCommand(migrateUpCmd, migrateDownCmd, migrateStatusCmd)
}

// withMigrateProvider loads config, opens the locked provider, runs fn and
// closes it afterwards so the session advisory lock is released rather than
// leaked — a leak here blocks the next migrator for its whole retry window.
//
// The migration set comes from migrationFS, injected by main so that no package
// under internal/ imports the root-level migrations package.
func withMigrateProvider(cmd *cobra.Command, fn func(ctx context.Context, p *goose.Provider) error) (err error) {
	cfg, err := config.Load(mustString(cmd, "config"), cmd.Flags())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.DB.DSN == "" {
		return errors.New("db.dsn is required (config file or FLOWHAND_DB_DSN)")
	}
	if migrationFS == nil {
		return errors.New("no migration set was injected; see cli.Execute")
	}

	ctx := cmd.Context()
	p, closeProvider, err := storage.NewGooseProvider(ctx, cfg.DB, migrationFS)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeProvider(); closeErr != nil && err == nil {
			err = fmt.Errorf("close migration provider: %w", closeErr)
		}
	}()

	return fn(ctx, p)
}

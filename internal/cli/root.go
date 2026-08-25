package cli

import (
	"context"
	"io/fs"
	"os"

	"github.com/spf13/cobra"
)

// migrationFS holds the goose migration set, injected by main. The embed
// directive has to live in the root-level migrations package (go:embed cannot
// name files above its own directory), and injecting it here keeps every
// package under internal/ free of that dependency.
var migrationFS fs.FS

var (
	// set by -ldflags at build time
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:     "flowhand",
	Short:   "Distributed task and workflow orchestration",
	Version: version,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "print flowhand version",
	Run: func(cmd *cobra.Command, _ []string) {
		cmd.Printf("flowhand %s (commit %s, built %s)\n", version, commit, date)
	},
}

func init() {
	rootCmd.SetOut(os.Stdout)
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true

	rootCmd.PersistentFlags().String("config", "", "path to config file")
	rootCmd.PersistentFlags().String("log-level", "info", "log level (debug|info|warn|error)")

	rootCmd.AddCommand(
		serverCmd,
		versionCmd,
		workerCmd,
		relayCmd,
		ingesterCmd,
		ctlCmd,
		migrateCmd,
	)
}

// Execute runs the root command and returns any error from the selected
// subcommand. migrations is the goose migration set the `migrate` subcommand
// applies.
//
// ctx carries signal cancellation: `migrate up` can sit for up to half an hour
// waiting on the session advisory lock, and a Ctrl-C there has to be able to
// give up rather than wait the window out.
func Execute(ctx context.Context, migrations fs.FS) error {
	migrationFS = migrations
	return rootCmd.ExecuteContext(ctx)
}

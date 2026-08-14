package cli

import (
	"os"

	"github.com/spf13/cobra"
)

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
	)
}

// Execute runs the root command and returns any error from the selected subcommand.
func Execute() error {
	return rootCmd.Execute()
}

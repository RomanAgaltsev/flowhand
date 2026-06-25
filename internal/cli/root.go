package cli

import (
	"fmt"
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
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Fprintln(os.Stdout, version)
	},
}

func init() {
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

func Execute() error {
	return rootCmd.Execute()
}

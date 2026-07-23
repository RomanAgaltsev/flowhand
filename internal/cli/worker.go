package cli

import (
	"os"

	"github.com/spf13/cobra"
)

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "not implemented",
	Run: func(cmd *cobra.Command, _ []string) {
		cmd.Println("not implemented")
		os.Exit(1)
	},
}

package cli

import (
	"os"

	"github.com/spf13/cobra"
)

var ingesterCmd = &cobra.Command{
	Use:   "ingester",
	Short: "not implemented",
	Run: func(cmd *cobra.Command, _ []string) {
		cmd.Println("not implemented")
		os.Exit(1)
	},
}

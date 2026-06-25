package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "not implemented",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Fprintln(os.Stdout, "not implemented")
		os.Exit(1)
	},
}

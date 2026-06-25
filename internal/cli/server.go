package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "starting server (stub)",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Fprintln(os.Stdout, "starting server (stub)")
		os.Exit(1)
	},
}

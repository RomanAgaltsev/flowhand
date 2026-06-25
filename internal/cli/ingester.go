package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var ingesterCmd = &cobra.Command{
	Use:   "ingester",
	Short: "not implemented",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Fprintln(os.Stdout, "not implemented")
		os.Exit(1)
	},
}

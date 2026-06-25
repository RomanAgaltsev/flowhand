package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var relayCmd = &cobra.Command{
	Use:   "relay",
	Short: "not implemented",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Fprintln(os.Stdout, "not implemented")
		os.Exit(1)
	},
}

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var ctlCmd = &cobra.Command{
	Use:   "ctl",
	Short: "not implemented",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Fprintln(os.Stdout, "not implemented")
		os.Exit(1)
	},
}

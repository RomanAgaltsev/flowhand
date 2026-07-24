package cli

import (
	"github.com/spf13/cobra"
)

var ctlCmd = &cobra.Command{
	Use:   "ctl",
	Short: "not implemented (Phase 1+)",
	RunE:  func(_ *cobra.Command, _ []string) error { return errNotImplemented },
}

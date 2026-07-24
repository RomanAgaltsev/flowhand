package cli

import (
	"github.com/spf13/cobra"
)

var ingesterCmd = &cobra.Command{
	Use:   "ingester",
	Short: "not implemented (Phase 1+)",
	RunE:  func(_ *cobra.Command, _ []string) error { return errNotImplemented },
}

package cli

import (
	"github.com/spf13/cobra"
)

var relayCmd = &cobra.Command{
	Use:   "relay",
	Short: "not implemented (Phase 1+)",
	RunE:  func(_ *cobra.Command, _ []string) error { return errNotImplemented },
}

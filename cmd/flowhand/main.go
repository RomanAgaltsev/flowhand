package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/RomanAgaltsev/flowhand/internal/cli"
	"github.com/RomanAgaltsev/flowhand/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error", err)
		os.Exit(1)
	}
}

// run exists so the signal handler's deferred stop actually runs; os.Exit in
// main would skip it.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The migration set is embedded at build time, so a deployed image needs
	// nothing but the binary itself — no migrations/ directory, no CWD
	// assumptions. It is injected here rather than imported by internal/cli so
	// the layering check stays honest about what depends on what.
	return cli.Execute(ctx, migrations.FS)
}

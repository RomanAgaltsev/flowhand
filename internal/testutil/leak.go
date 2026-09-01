// Package testutil holds the shared goroutine-leak check that every
// leak-checked package in flowhand calls from a one-line TestMain.
//
// It is a test-only leaf: nothing in production imports it.
package testutil

import (
	"testing"

	"go.uber.org/goleak"
)

// VerifyMain runs a package's tests and then asserts that no goroutine
// outlived them.
//
// The ignore list below is shared by every leak-checked package in flowhand.
// It is discovered, not designed: when a package legitimately leaves a
// background goroutine running for the process lifetime, its top-of-stack
// function is added here. See the procedure in
// plans/phase-1/01-p0-dependencies.md, Step 2 — add exactly one ignore per
// observed failure, and never one you have not personally seen fire.
func VerifyMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// Windows only: go-winio's I/O completion processor, started by its package
		// init (the Docker named-pipe transport testcontainers pulls in) and alive
		// for the process lifetime. AnyFunction rather than TopFunction because the
		// goroutine sits blocked in a syscall whose stdlib frame is the top of the
		// stack — which syscall that is varies with timing. On the Linux CI runner
		// the goroutine never exists and this matches nothing. Seen firing in
		// internal/storage/txmgr, the first package to open a pool under this check.
		goleak.IgnoreAnyFunction("github.com/Microsoft/go-winio.ioCompletionProcessor"),
	)
}

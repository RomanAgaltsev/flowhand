// Package testutil holds the shared goroutine-leak check that every
// leak-checked package in flowhand calls from a one-line TestMain.
//
// It is a test-only leaf: nothing in production imports it.
package testutil

import (
	"sync"
	"testing"

	"go.uber.org/goleak"
)

var (
	afterRunMu sync.Mutex
	afterRun   []func()
)

// AfterRun registers fn to run once the package's tests have finished and before
// the leak check, for what a whole test binary shares: a fixture stopped by the
// test that started it would be gone for the next one, and one never stopped
// leaves its goroutines for the check to find.
func AfterRun(fn func()) {
	afterRunMu.Lock()
	defer afterRunMu.Unlock()
	afterRun = append(afterRun, fn)
}

// runner is the testing.M goleak sees: tests first, then the AfterRun hooks.
type runner struct{ m *testing.M }

// Run satisfies goleak.TestingM.
func (r runner) Run() int {
	code := r.m.Run()

	afterRunMu.Lock()
	defer afterRunMu.Unlock()
	for _, fn := range afterRun {
		fn()
	}
	return code
}

// VerifyMain runs a package's tests and then asserts that no goroutine
// outlived them.
//
// The ignore list below is shared by every leak-checked package in flowhand.
// It is discovered, not designed: when a package legitimately leaves a
// background goroutine running for the process lifetime, its top-of-stack
// function is added here. Add exactly one ignore per failure you have seen fire,
// named for the goroutine's top function, with a comment saying who starts it and
// why it lives for the process.
func VerifyMain(m *testing.M) {
	goleak.VerifyTestMain(runner{m},
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

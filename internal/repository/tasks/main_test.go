//go:build integration

package tasks_test

import (
	"testing"

	"github.com/RomanAgaltsev/flowhand/internal/testutil"
)

// VerifyMain is what stops the package's shared Postgres (see pgtest).
func TestMain(m *testing.M) { testutil.VerifyMain(m) }

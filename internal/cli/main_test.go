//go:build integration

package cli

import (
	"testing"

	"github.com/RomanAgaltsev/flowhand/internal/testutil"
)

// VerifyMain is what stops the package's shared Postgres (see pgtest).
func TestMain(m *testing.M) { testutil.VerifyMain(m) }

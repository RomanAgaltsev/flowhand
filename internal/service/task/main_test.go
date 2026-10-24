package task

import (
	"testing"

	"github.com/RomanAgaltsev/flowhand/internal/testutil"
)

// No build tag, unlike txmgr's: this package has unit tests too, and the
// store test opens a real Redis client whose pool must not outlive it.
func TestMain(m *testing.M) { testutil.VerifyMain(m) }

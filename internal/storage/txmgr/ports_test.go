package txmgr

import (
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
)

// Compile-time proof that Manager satisfies service/task.TxRunner, the port
// the composition root passes to task.NewCommander. It lives here rather than
// beside the type in manager.go because asserting it there requires importing
// service/task from production storage code — an infrastructure→service arrow
// .go-arch-lint rightly rejects, and which its config cannot scope down to
// "assertions only". A test file is exempt from the arch check (excludeFiles),
// so the arrow exists only where the compiler needs it.
//
// Untagged on purpose: this compiles on every `go test ./...` and `go vet`
// run, so port drift still fails at the definition site — in this package,
// naming the missing method — rather than at the composition root.
var _ task.TxRunner = (*Manager)(nil)

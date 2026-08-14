# Import rules

flowhand is layered. The layering is only real if it is enforced, so this page
states the rules in plain English, points at the two tools that check them, and
says what to do when a rule genuinely needs to bend.

Rationale for choosing `go-arch-lint` over the alternatives is recorded in
ADR 0003 (`adr/0003-go-arch-lint-for-layered-architecture.md`), which lives in
the private design vault rather than this repo.

## The layers

```
        cmd/flowhand
             │
        internal/cli          ← composition root: the ONLY place that sees every layer
             │
   ┌─────────┼──────────┬───────────────┐
   │         │          │               │
internal/  internal/  internal/     internal/
 server      api       service        storage
             │           │               │
             │      ┌────┴────┐          │
             │      │         │          │
        internal/  internal/ internal/   │
          oas     repository  client     │
                       │         │       │
                       └────┬────┘       │
                            │            │
                     internal/domain ────┘   (leaf)
```

## The rules

1. **`internal/domain` is a leaf.** A domain package may import the standard
   library and small generic helpers (`uuid`), and a bounded context
   (`domain/tasks`) may import the shared `domain` package for the `Event`
   contract. It must import nothing else from `internal/`.
2. **A bounded context never imports a sibling.** `domain/tasks` must not import
   `domain/workflows`. Cross-context work belongs in the service layer.
3. **`internal/api` never touches persistence.** No `pgx`, no `pgxpool`, no
   `internal/storage/queries`, no `internal/repository/*`. Transport depends on
   service-layer interfaces and on the domain types that cross the boundary.
   This is what the Commander/Querier split buys.
4. **`internal/service` never touches a driver.** No `pgx`, no sqlc. Services
   depend on the repository *ports* they declare in `ports.go` and on
   `TxRunner` for transaction scope.
5. **`pgx` is importable only by `internal/storage` and `internal/repository`.**
   Everything above them sees domain sentinels (`ErrNotFound`, `ErrConflict`),
   never SQLSTATE codes or driver types. `internal/repository/tasks.translate`
   is where that conversion happens.
6. **No transaction is held across non-Postgres I/O.** A `WithinTx` closure must
   not make HTTP, Redis, Kafka, etcd or SeaweedFS calls.
7. **Nothing under `internal/` imports `internal/cli` or `cmd/`.** Wiring flows
   one way.

## What enforces them

Two tools, deliberately overlapping, checked by `task lint` and `task lint:arch`
and by both CI jobs:

| Tool | Config | Enforces |
|---|---|---|
| `go-arch-lint` | `.go-arch-lint.yml` | rules 1–4, 7 — the component dependency graph |
| `depguard` (via golangci-lint) | `.golangci.yml` | rules 3–5 — specific banned import paths per directory |

```bash
task lint        # golangci-lint, incl. depguard
task lint:arch   # go-arch-lint check
```

Rules 2 and 6 are not machine-checked today. Rule 6 is a review-time concern;
rule 2 becomes checkable as soon as a second bounded context has real code.

## Two traps in `.go-arch-lint.yml`

Both of these were live defects in this repo, and both fail in ways that look
like success. They are called out here because the second one is not documented
upstream at all.

**`mayDependOn: []` is a schema error, not "depends on nothing".** It makes
`go-arch-lint check` exit 1 with a config-validation message *before it analyses
a single import*. If the CI step is not being watched, the entire fitness
function silently does nothing while the config looks correct. A leaf component
is expressed by **omitting it from `deps:`**:

```yaml
deps:
  api:
    mayDependOn: [oas, service-task, domain-tasks]
  # domain, oas, queries, client and config are leaves — deliberately absent.
```

**A parent component does not cover its sub-packages as import targets.**
Declaring `service: { in: [service, service/**] }` attaches
`internal/service/task`'s *files* to the `service` component, but naming
`service` in another component's `mayDependOn` does **not** permit importing
`internal/service/task`. The result is a violation report that looks like the
layering is broken when the config is. Give every package its own component:

```yaml
components:
  service-task:     { in: service/task }
  service-workflow: { in: service/workflow }
```

Verbose, but unambiguous — and the component list ends up being the import graph
written down, which is worth having anyway.

## Verifying the gate actually fires

A gate you have never watched fail is a gate you are trusting on faith. Drop a
deliberate violation in, confirm it is caught, then remove it:

```bash
cat > internal/domain/tasks/scratch_violation.go <<'EOF'
package tasks

import _ "github.com/RomanAgaltsev/flowhand/internal/repository"
EOF

task lint:arch
# Component domain-tasks shouldn't depend on .../internal/repository
#   in .../internal/domain/tasks/scratch_violation.go:3
# exit status 1

rm internal/domain/tasks/scratch_violation.go
task lint:arch   # OK - No warnings found
```

Do this after any edit to `.go-arch-lint.yml`. Both traps above pass a casual
"it exits 0" check while enforcing nothing.

## When a rule needs to bend

The escape hatch is a lint-disable comment **with a justification naming why the
layering does not apply**. It is expected in exactly one place — the composition
root in `internal/cli/server.go`, which by definition sees every layer, and which
`.go-arch-lint.yml` already grants that access explicitly rather than by
suppression.

Anywhere else, a needed suppression is a signal that either the layering or the
design is wrong. Change the design, or change the rule and this document
together — never suppress silently.

```go
//nolint:depguard // composition root: constructing the storage layer is this file's job
```

# Layers

*Written 2026-10-28, against `feat/task-engine` at `866adf8`.* This page says what each layer
is for and what breaks when that job leaks elsewhere. What is enforced, and how, is
[`import-rules.md`](import-rules.md). Where errors change shape is
[`error-style.md`](error-style.md). Both rationales are ADRs in the private design vault.

## What each layer is for

**`internal/api`** speaks HTTP. It decodes requests into commands, calls the Commander or the
Querier, and maps results onto the OpenAPI contract
([`handler.go`](../../../internal/api/handler.go)). It decides nothing about tasks.

**`internal/service`** holds use cases. It knows that a submit is one atomic unit with an
idempotency check in front, and it states that through ports it declares itself
([`ports.go`](../../../internal/service/task/ports.go)).

**`internal/domain`** holds the rules: which transitions are legal, what a valid priority is,
which events a transition emits. It does no I/O, so every rule is testable in microseconds.

**`internal/repository`** moves aggregates in and out of Postgres, one package per aggregate or
table family, and turns rows back into domain values.

**`internal/storage`** owns the mechanics: the pool, generated queries, migrations and the
transaction manager. It holds no decisions.

## What goes wrong if you don't

**pgx in the API layer.** The handler starts branching on driver errors, and the HTTP contract
now changes whenever the driver does. Worse, a handler holding a pool can write without the
Commander, which is the next failure.

**Business logic in a repository.** Say `PersistTransition` decided whether `running →
succeeded` is legal. That rule would then exist in SQL and in `Task`, the two copies would
drift, and only an integration test against a real database could exercise it. Today the
aggregate decides and the repository persists whatever it is handed. The one check in SQL is
the lease-epoch fence, because only the `UPDATE` can make it atomically.

**A repository reached from a handler.** It skips everything the Commander guarantees: the
idempotency check, the transaction, and the outbox append. The result is a task row that no
event ever announces, which is the exact gap the outbox exists to close.

**A god-storage package.** One package holding every aggregate's persistence becomes the place
every change touches and every layer imports. The arch-lint graph can no longer tell task
persistence from workflow persistence, and bounded contexts merge at the bottom of the stack.
sqlc's generated `queries` package is shared, but it is generated and makes no decisions. The
decisions live in the per-aggregate repositories.

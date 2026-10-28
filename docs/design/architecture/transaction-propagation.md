# Transaction propagation

*Written 2026-10-28, against `feat/task-engine` at `866adf8`.* The decision it
elaborates, Postgres as the state of truth with a transactional outbox, is ADR 0001
(`adr/0001-postgres-as-state-of-truth.md`) in the private design vault. The code is
[`internal/storage/txmgr/manager.go`](../../../internal/storage/txmgr/manager.go)
and [`internal/repository/dbtx.go`](../../../internal/repository/dbtx.go).

## The problem

flowhand's outbox pattern requires that a task row and its domain events become durable
together. Two writes, one transaction, or the guarantee is worthless: a crash between them
leaves a task that succeeded and an event nobody will ever receive.

That is easy to say and awkward to arrange. The service layer is where "these two things are
one operation" is known. The repositories are where the writes happen. And `pgx.Tx` is a
persistence type that neither the service layer nor the API should ever name. The moment
`Submit(ctx, tx, cmd)` appears, the use case knows what database we use.

## Three ways to arrange it

**Ad-hoc at the call site.** The Commander opens a transaction and passes it down. Honest and
explicit, and it puts `pgx.Tx` in every signature between the service and the database. The
layering is now decorative: `internal/service` imports pgx, and the interface boundary buys
nothing.

**`BeginTx` inside the repository.** Each repository manages its own transaction, so no pgx
escapes. It also makes the thing we actually need impossible. Two repositories cannot share a
transaction they each opened privately, so the task row and the outbox row commit separately
and the outbox guarantee evaporates. This option looks cleanest and fails hardest.

**Context propagation.** The service says "these writes are one unit" through a port that names
no driver, `TxRunner.WithinTx(ctx, fn)`. The transaction travels inside the `ctx` the closure
receives. Its failure mode is the one every context value has: the dependency is invisible in
the signature. A repository called outside `WithinTx` does not fail. It quietly runs on the
pool, in autocommit. We accept that cost, and most of the gotchas below are its consequences.

## The chosen pattern

Repositories never begin anything. They ask a `Resolver` what to query against:

```go
func (m *Manager) Resolve(ctx context.Context) repository.DBTX {
	if tx, ok := ctx.Value(ctxKey{}).(pgx.Tx); ok {
		return tx
	}
	return m.pool
}
```

`DBTX` is the method set sqlc's generated code calls, and both `*pgxpool.Pool` and `pgx.Tx`
satisfy it, so each repository is written once and works either way. The service draws the
boundary:

```go
err = c.tx.WithinTx(ctx, func(ctx context.Context) error {
	if err := c.tasks.Insert(ctx, t, key); err != nil {
		return err
	}
	for ev := range t.PullEvents() {
		if err := c.outbox.Append(ctx, ev); err != nil {
			return err
		}
	}
	return nil
})
```

## Gotchas

- **Nesting reuses; it does not nest.** A second `BEGIN` inside an open transaction would wait
  on locks the outer one holds, on a connection the outer one has checked out. That is a hang,
  not an error. So an inner `WithinTx` runs on the outer transaction, and an inner error rolls
  back everything. The inner call's `TxOptions` are **ignored**: an inner request for
  SERIALIZABLE silently runs at the outer level. If partial rollback is ever needed, it gets
  its own `WithinSavepoint`, not a new meaning for `WithinTx`.
- **A panic rolls back and keeps panicking.** `pgx.BeginTxFunc` defers the rollback, so the
  connection returns to the pool clean. The manager does not recover, because recovering would
  let the caller believe the work committed.
- **One failed statement poisons the transaction.** After a unique violation, Postgres rejects
  every further statement with "current transaction is aborted". That is why the Commander's
  idempotent replay read runs after `WithinTx` returns, on the pool.
- **The transaction's `ctx` is not for goroutines.** A `pgx.Tx` is one connection and is not
  safe for concurrent use. Fanning out inside the closure races on that connection, and a
  goroutine that outlives the closure finds the transaction closed.
- **Locks need the transaction.** `GetForUpdate` outside `WithinTx` is a plain read whose lock
  is released when the statement returns. The port documents this, and nothing enforces it.

## Tracing with otelpgx

The pool carries an `otelpgx` tracer
([`internal/storage/pool.go`](../../../internal/storage/pool.go)), which makes a span of every
statement. That includes `BEGIN`, `COMMIT` and `ROLLBACK`, which pgx sends as ordinary
statements, and each span is parented on whatever span is in the `ctx` passed in. Propagation
composes with this for free. `WithinTx` derives the closure's context from the caller's with
`context.WithValue`, so the request span survives. A trace of `createTask` shows `BEGIN`, the
inserts, the outbox batch and `COMMIT` nested under it, and the width of that bracket is
lock-hold time. The same context gives each outbox row its
`trace_id`, which links the event a consumer receives back to the request that caused it.

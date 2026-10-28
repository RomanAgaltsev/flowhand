# Isolation matrix

*Written 2026-10-28, against `feat/task-engine` at `866adf8`.* It elaborates the transaction
rules in [`transaction-propagation.md`](transaction-propagation.md) and the schema's hot/cold
split (`design/schema.md`, private design vault). Every transaction today goes through
`WithinTx`, which passes `pgx.TxOptions{}`, Postgres's default of READ COMMITTED
([`internal/storage/txmgr/manager.go`](../../../internal/storage/txmgr/manager.go)).

| Operation | Runs on | Level | Why this level |
|---|---|---|---|
| `Commander.Submit`: task row, attempts, ledger row, outbox append | `WithinTx` | READ COMMITTED | inserts only; the ledger's primary key settles duplicates |
| `TasksRepo.GetForUpdate` then `PersistTransition` (no caller until the engine lands) | `WithinTx` | READ COMMITTED + `FOR UPDATE` | the row lock serializes writers; the fence's row count is the answer |
| `TasksRepo.Get`: hot table, then `tasks_archive` | the pool, autocommit | READ COMMITTED | a fresh snapshot per statement is what makes the order safe |
| *Phase 2:* `FanIn` | `WithinTxOpts` | SERIALIZABLE | not built yet |

## The submit transaction

Submit reads nothing inside its transaction; it inserts under a fresh UUIDv7. Two submits racing
on one idempotency key are settled by the ledger's primary key: the second insert waits for the
first, then fails with a unique violation, at any level. A higher level would only add
serialization failures to retry.

## The transition pair

`GetForUpdate` locks the row, and `PersistTransition` writes it with
`WHERE id = $1 AND lease_epoch = $5`. Under READ COMMITTED a blocked `UPDATE` re-evaluates its
`WHERE` against the newest row version, so a scheduler that lost ownership gets **zero rows**,
an answer the caller already branches on. REPEATABLE READ would turn the same lost race into a
serialization error: the same outcome, harder to handle.

## The read: hot, then archive

The archive trigger moves a row inside the one `UPDATE` that makes a task terminal. `Get` runs
its two lookups as separate statements, each with a new snapshot. If the hot read misses, the
move had already committed, so the later archive read finds the row. Reverse the order and a
task archived *between* the reads is in neither table: a false 404.

One edge is latent. `Get` loads the row, then its attempts, in two snapshots, so a transition
committing between them yields attempts one step ahead of the status. Nothing on this path
reads attempts today; the first caller that does should wrap `Get` in a read-only REPEATABLE
READ transaction.

## What Phase 2 adds

Fan-in asks "was I the last parent to finish?" of a count two finishers can read at once:
write skew, which only SERIALIZABLE prevents. It will use `WithinTxOpts`, retry on 40001, and
must be the outermost transaction, since a nested call inherits the outer level.

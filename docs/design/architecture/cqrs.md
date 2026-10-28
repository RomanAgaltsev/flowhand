# CQRS-lite

*Written 2026-10-28, against `feat/task-engine` at `866adf8`.* The design it elaborates is the
cache-invalidation catalog (`design/cache-invalidation.md`) in the private design vault. The
code is [`internal/service/task/commander.go`](../../../internal/service/task/commander.go) and
[`querier.go`](../../../internal/service/task/querier.go).

## What it is, and what it is not

CQRS separates the path that changes state from the path that reads it, so each can take the
shape its job needs. It says nothing about where the truth lives.

**Event sourcing** is a different decision: the event log *is* the truth, and current state is
a fold over it. flowhand emits domain events and writes them to an outbox, which sounds like
event sourcing and is not. Postgres rows are the source of truth. An event is a notification
of a transition that already happened, written in the same transaction so it cannot be lost.
Nothing replays events to rebuild a task. Delete the drained outbox rows and no task loses a
byte of state. That is the test.

## flowhand's split

`POST /v1/tasks` goes through the **Commander**, which validates, builds the aggregate, and
writes the row and its events in one transaction. `GET /v1/tasks/{id}` goes through the
**Querier**, which returns a `TaskView` (four fields, status as a plain string), not the
aggregate. It is "lite" because both sides share one database and one repository. What the
split buys today is a read side that can grow its own caching and shape without touching the
write path.

## The cache, and why it has no staleness budget

The Querier caches in an in-process LRU, but only tasks in a **terminal** status:

```go
if t.IsTerminal() {
	q.cache.Put(id, v, q.ttl)
}
```

That `if` is the whole invalidation strategy. A terminal task never changes, so nothing cached
can go stale, and `TaskCache` has no `Forget`. The TTL only ages entries out.

An earlier design cached every status, made the Commander evict on write, and promised a
staleness budget: no read older than the LRU's TTL. It failed for a structural reason. The
Commander writes a task once, at submit. Every later transition belongs to the scheduler's
paths for success, failure, retry, reclaim, dead-lettering and cancellation, and none of them
holds a cache reference. A budget is a promise about how wrong a read may be, and keeping it
means knowing every writer, and Phase 2's DAG engine adds another. Each would widen the window
silently. Caching only immutable states makes the question disappear, across pods
too: a terminal task looks the same everywhere.

## Reading two tables

A trigger moves a task's row from `tasks` to `tasks_archive` in the same `UPDATE` that makes it
terminal. So `TasksRepo.Get` reads the hot table first, then the archive. The
[isolation matrix](isolation-matrix.md) says why that order is the safe one.

That is what makes the terminal-only rule cheap. The tasks the cache may hold are exactly the
archived ones, whose uncached read costs the most: a miss on `tasks`, a hit on `tasks_archive`,
then the attempts. One reader still checks the hot table alone. The Commander's idempotent
replay looks a key up in `tasks`, so it cannot see an archived task yet.

## When full CQRS earns its keep

Phase 4's read model is a separate store, `readmodel.tasks_view`, projected from `task.*.v1`
events by a Kafka consumer. `GET` moves onto it, and `TaskView` keeps its shape over a new
source. That cost is worth paying once reads want shapes the write model lacks, like lists,
filters and per-tenant views, or enough load to need their own store.

There, a staleness budget is the **right** tool. A projection is stale by construction, by
exactly the consumer's lag, and the honest move is to bound it: ≤ 10 s, from the
event-delivery SLO. Phase 1's cache sits in front of a database it can always query, so it has
no excuse for staleness. The read model does have a reason, so it gets a budget.

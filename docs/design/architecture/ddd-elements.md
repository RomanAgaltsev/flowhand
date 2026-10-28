# DDD elements

*Written 2026-10-28, against `feat/task-engine` at `866adf8`.* It elaborates the "DDD-lite"
vocabulary of the root design and the event-storming notes (`design/event-storming.md`), both
in the private design vault. The code is
[`internal/domain/tasks/`](../../../internal/domain/tasks/).

## What flowhand picks up

**Three types per concept.** A task exists as a sqlc row, a domain `Task` and an `oas.Task` on
the wire, with a mapper at each boundary
([`repository/tasks/mapper.go`](../../../internal/repository/tasks/mapper.go),
[`api/mapper.go`](../../../internal/api/mapper.go)). A schema change stops at the repository,
and a wire change stops at the API.

**Aggregates.** `Task` is the root and owns its `Attempt`s. Every transition, such as `Start`,
`Complete` or `ScheduleRetry`, is a method that checks legality and records an event. Loading
from the database goes through `FromPersistence`, which emits nothing, so a read never
re-announces a submission.

**Value objects.** `Priority`, `IdempotencyKey`, `Handler`, `LeaseEpoch` and `ShardID` replace
bare primitives. A `Handler` from the wire is checked against a catalog, so an unknown handler
is a rejected command, not a row that fails later.

**Domain events, interlocked with the outbox.** The aggregate buffers events. The Commander
drains them with `PullEvents` inside the same transaction as the row write, and appends each to
the outbox ([`transaction-propagation.md`](transaction-propagation.md)). The domain decides
*what* happened. The transaction decides *whether* anyone hears of it. An event carries values,
never a `*Task`, so it describes the transition rather than whatever the task looks like later.

**One event type, not six.** The textbook default is a type per event: `TaskSubmitted`,
`TaskStarted`, and so on. flowhand started there and collapsed them into one `tasks.Event` with
a `Kind`. Nothing outside the package ever told the six apart, with no type switch and no
assertion, so they were one type wearing six names. The single type buys one encoder, one
outbox schema, and the `exhaustive` linter over `Kind`: add a seventh transition and CI names
every switch that misses it. Six unrelated structs cannot offer that guarantee at any price.

**Bounded contexts.** `tasks`, `workflows` and `schedules` are sibling packages that may not
import each other ([`import-rules.md`](import-rules.md)). Only `tasks` has code today.

## What flowhand skips, and why

**The formal repository pattern.** Evans' repository is a collection: add, find, remove,
always the whole aggregate, often behind a unit of work that tracks changes. flowhand's
repositories are ports shaped by use cases. `PersistTransition` takes a fence epoch and returns
a row count, because fencing is a concurrency check that only the `UPDATE` can make
atomically. `AttemptsRepo.Heartbeat` will write a child row without loading the aggregate,
because heartbeats are frequent and protect no invariant. A collection-shaped `Save(task)`
would hide both. There is no unit of work either: the transaction already is one.

**The application-service / domain-service split.** The textbook separates orchestration
(application services) from rules that belong to no single aggregate (domain services).
`internal/service/task` holds the Commander and Querier, which are application services in
all but name. There are no domain services because no rule spans aggregates yet. The first
candidate is Phase 2's fan-in, where a `WorkflowRun` reacts to its `Task`s finishing, and the
split will be decided there. Creating the package now would give it nothing to hold.

**Anti-corruption layers.** An ACL protects a model from a foreign one, typically a legacy
system or a vendor API whose concepts would leak in. flowhand has no upstream model to defend
against: it is the system of record. Its foreign vocabularies are Postgres and HTTP, and both
already have translation points: the mappers above and the error translation in
[`error-style.md`](error-style.md). Kafka consumers are downstream, so they adapt to us. An ACL
here would be a mapper wrapped in another mapper.

**Event sourcing,** for the reasons in [`cqrs.md`](cqrs.md): the row is the truth, and events
only announce it.

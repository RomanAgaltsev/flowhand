# flowhand

**Distributed task & workflow orchestration service in Go** — built as a single,
self-contained service that fuses a task queue (priorities, retries, cron,
idempotency) with a declarative workflow DAG engine (fan-out, fan-in,
conditionals). Think "Celery + Airflow in one Go binary."

> Interview-prep / portfolio project. A deliberate, marathon-scope backend
> built to exercise the skill gaps that senior Go backend interviews probe —
> advanced concurrency, distributed systems, low-level Go, DSA, and system
> design — through one coherent, runnable system rather than scattered toys.

## Product framing

Flowhand is framed as the backend of a **Stripe-style webhook and notification
delivery platform**. External services POST events; flowhand fans each event
out to its subscriber set (HTTP webhooks, email, push, Slack), retries on
failure, and guarantees each subscriber receives each event **effectively
once**.

This domain was chosen because it legibly justifies every architectural choice:

- **idempotency** — producers retry webhooks
- **transactional outbox** — we promise delivery
- **DAG workflows** — one event → N subscribers → per-channel transforms
- **effectively-once end-to-end** — duplicate deliveries break customers
- **rate limiting** — politeness to downstream endpoints
- **DLQ** — bad endpoints must not block the queue

Read "task" as *one delivery attempt to one subscriber* and "workflow" as
*one event's fan-out to its subscriber set*.

## Status

Early stage / work-in-progress. The full **dev infrastructure and observability
stack** (Docker Compose) and the **Go toolchain scaffolding** (module,
Taskfile, linter, migration/vuln targets) are in place; the application code
is being built out phase by phase.

## Features

### Tasks

- Priority levels, scheduled delays, and **cron** schedules
- **Idempotency keys** (Redis fast-path + Postgres `UNIQUE` as durable floor),
  scoped per `(tenant, handler, key)`
- Retries with exponential backoff + jitter, per-task timeouts
- Heartbeats + lease extension for long-running handlers
- **Dead-letter queue** (DLQ) for poison messages
- Per-handler concurrency caps and rate limiting

### Workflows

- Declarative **DAG** definitions with versioning
- Fan-out / fan-in, conditional branches, workflow context
- Cascading cancellation, cycle detection, topological scheduling
- `on_failure` compensation (saga-style)

### Delivery guarantees

- **Effectively-once end-to-end**: at-least-once infrastructure + consumer-side
  dedup by `event_id`
- Transactional outbox → no lost events on crash
- Chaos-tested recovery from leader loss, worker loss, and partitions

### Handler catalog

A deliberately diverse set of registered handlers, each exercising a property
the system must handle:

| Handler | Exercises |
| --- | --- |
| `webhook.deliver` | retries, backoff, circuit breaker, per-endpoint rate limit |
| `email.send` | timeouts, idempotency semantics on retries |
| `report.build` | long-running work, heartbeats, lease extension |
| `aggregate.count` | DAG fan-in, `SERIALIZABLE` isolation for the race |
| `notify.broadcast` | recursive fan-out producing child tasks |
| `flaky.tester` | deterministic N% failures for retry/chaos demos |
| `image.thumbnail` | large payloads (object-storage spill), CPU-bound work |
| `slow.leak` | goroutine leaks for goleak/pprof demos |

## Architecture

### Process topology — single binary, multiple subcommands

```
flowhand server     # control plane: HTTP+gRPC API, scheduler, DAG engine
flowhand worker     # executes task handlers, talks to server over gRPC
flowhand relay      # tails Postgres outbox -> publishes to Kafka
flowhand ingester   # (stretch) Kafka -> Postgres bulk ingress
flowhand ctl        # CLI: submit / inspect / drain
```

N instances of each run independently. The control-plane server hosts the API,
the leader-elected scheduler, and the DAG engine; workers pull work over gRPC
streams and report results back.

### Data flow (submit + execute one task)

1. Client `POST /v1/tasks` with an idempotency key.
2. Redis idempotency check; on hit, the existing task id is returned.
3. One Postgres transaction: insert `tasks` + `outbox_events` +
   `NOTIFY tasks_available` (**transactional outbox**).
4. The scheduler leader (woken via `LISTEN`) dequeues with
   `SELECT ... FOR UPDATE SKIP LOCKED` scoped to its shards, respects DAG
   dependencies, and dispatches to a worker over a server-streaming gRPC.
5. The worker executes the handler and streams progress/heartbeat/result back
   over a client-streaming gRPC.
6. State transition is written in a Postgres txn (task update + outbox event).
7. The relay polls the outbox, publishes to Kafka in batches (at-least-once),
   and marks rows sent.
8. Downstream Kafka consumers process independently with consumer-side
   idempotency.

### Components

- **Control plane** — OpenAPI-first REST API (ogen), leader-elected scheduler,
  DAG state engine.
- **Workers** — bounded handler pools, lease renewal, graceful drain.
- **Postgres** — source of truth + transactional outbox (SQL-backed queue via
  `SKIP LOCKED`, `LISTEN/NOTIFY` wake-up, range partitioning, `pg_uuidv7`).
- **Redis** — rate limiting, idempotency fast-path, dedup, hot cache tier.
- **etcd** — leader election (lease + fencing tokens), sharding, live
  membership/rebalance via watch.
- **Kafka** — event bus fed by the outbox relay.
- **SeaweedFS** — S3-compatible object storage for large payloads (> 64 KB)
  and pre-signed URLs.

## Patterns & methodologies

The codebase deliberately stays "lite" on ceremony — enough structure to get
testability and clear seams, without the overhead of full frameworks.

- **DDD-lite** — bounded contexts (`tasks`, `workflows`, `schedules`) with
  aggregate roots and a shared ubiquitous language. No event sourcing, no
  domain framework.
- **CQRS-lite** — a **Commander** (writes: state change + outbox in one
  transaction) separated from a **Querier** (reads: repo + process-local
  cache), sharing one domain model and one database.
- **Three-types pattern** — explicit separation of *storage row* (sqlc) ↔
  *domain model* ↔ *transport DTO*, with mapper adapters at each boundary.
- **Transactional outbox** — the only correct way to atomically update the DB
  and publish an event.
- **`SKIP LOCKED` dequeue** — the canonical SQL-backed queue.
- **Lease-based leader election + fencing tokens** — split-brain writes are
  rejected by a monotonic epoch checked on every scheduler→DB write.
- **Consistent-hash sharding + live rebalance** — rebalance touches ≤ 1/N of
  keys; cluster membership arrives via etcd watch.
- **Effectively-once end-to-end** — at-least-once delivery plus consumer-side
  dedup.
- **Backpressure & bulkheading** — per-handler pools and per-shard streams so a
  slow handler can't starve others.
- **Sagas / compensation** — `on_failure` steps cleanly reverse partial work.

### Concurrency depth (a primary focus)

Bounded worker pools + backpressure, semaphores, `errgroup` with structured
cancellation, `singleflight`, fan-out/fan-in, staged pipelines, atomic
counters, `sync.Map` vs `RWMutex`+map, `atomic.Pointer` copy-on-write config,
`sync.Pool`, striped/per-key locks, channel-based state machines, and a
lock-free SPSC ring buffer — all under goroutine-leak detection (`goleak`) and
the race detector in CI.

### Low-level Go depth

Generic containers (`heap`, `ring`, `LRU`, `set`) as first-class primitives,
zero-copy `string`↔`[]byte` via `unsafe`, a lock-free SPSC ring built on
`unsafe.Pointer` + atomic cursors, and an AMD64 Plan 9 assembly `xxhash64`
with a pure-Go fallback build tag.

## Observability

Full **LGTM + Pyroscope** stack, provisioned automatically by the dev Compose:

- **Metrics** — OTel → Prometheus; RED on every endpoint, USE on every
  resource, exemplars linking histograms to traces. Strict cardinality
  discipline (no high-cardinality IDs as labels).
- **Traces** — OTel → OTLP → Tempo; propagation across HTTP, gRPC, and Kafka
  record headers; DB spans via `otelpgx`.
- **Logs** — `log/slog` with structured fields carrying `trace_id`/`span_id`;
  shipped to Loki.
- **Profiling** — `pprof` + `fgprof` on every process, continuously scraped by
  Pyroscope.

Grafana comes pre-provisioned with datasources (Prometheus, Loki, Tempo,
Pyroscope) and cross-links (traces ↔ logs ↔ metrics ↔ profiles).

## Capacity targets

- 5,000 tasks/s submit peak
- 2,000 tasks/s execute sustained
- ~2 KB average payload
- p99 submit latency < 50 ms
- p99 end-to-end < 5 s for simple handlers
- 90-day archive retention

## Tech stack

| Area | Choice |
| --- | --- |
| Language | Go 1.26 (generics first-class) |
| REST API | **ogen** (OpenAPI-first codegen) over `net/http` |
| gRPC | `google.golang.org/grpc` + **buf** + `go-grpc-middleware/v2` |
| Database | **pgx/v5** (direct), **goose v3** migrations, **sqlc** |
| Redis | `redis/go-redis/v9` + `samber/hot` cache tier |
| Kafka | `twmb/franz-go` |
| Object storage | `aws-sdk-go-v2/service/s3` over **SeaweedFS** |
| Coordination | `etcd/client/v3` + `concurrency` (Election, Mutex, Session) |
| Logging | `log/slog` + `samber/slog-{multi,formatter,sampling}` |
| Tracing/Metrics | OpenTelemetry + `prometheus/client_golang` |
| Profiling | `pprof`, `fgprof`, **Pyroscope** |
| Errors | `samber/oops` (structured, with stack) |
| Concurrency | `x/sync` (`errgroup`, `semaphore`, `singleflight`), `sourcegraph/conc` |
| Cron | `adhocore/gronx` |
| Testing | Ginkgo, `testcontainers-go`, `gopter` (property-based), **k6** load, `goleak`, `benchstat` |

**Deliberately excluded** to keep the stack stdlib-first and dependency-light:
GORM/ent/sqlx, chi/gin/echo/fiber, zap/zerolog/logrus, RabbitMQ/NATS, Consul,
gorilla/mux.

## Dev environment

The entire infra and observability stack runs in Docker Compose:

```sh
task up      # bring up the dev stack (postgres, redis, etcd, kafka, seaweedfs,
             #                   prometheus, grafana, tempo, loki, promtail, pyroscope)
task down    # tear it down
task logs    # tail stack logs
```

Other Taskfile targets:

```sh
task build          # build the flowhand binary
task test           # go test -race ./...
task lint           # golangci-lint run
task vuln           # govulncheck ./...
task run:server     # build + run the control-plane server
task migrate:up     # apply database migrations
task migrate:down   # roll back one migration
```

## Roadmap (high level)

0. **Foundations** — repo layout, tooling, full dev stack, API/migration/OTel
   scaffolding.
1. **Single-node task engine** — full task lifecycle, idempotency, retries,
   cron, DLQ, outbox → Kafka, gRPC dispatch.
2. **Workflow DAG engine** — DAG executor, fan-out/fan-in, conditionals,
   cycle detection, property-based tests.
3. **Distributed coordination** — etcd leader election + fencing, consistent
   sharding, live rebalance, chaos drills.
4. **Production polish** — object-storage payloads, Kafka consumer suite,
   SLOs + dashboards, k6 load tests to target, pprof-driven optimization,
   low-level Go exercise tracks.
5. **Stretch** — Web UI, remote handler protocol, own Raft (optional).

---

*Built by Roman Agaltsev.*

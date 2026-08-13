# Getting started

Bring up flowhand, create a task through the API, and watch the request flow
through Tempo and Loki. Takes about five minutes from a clean clone.

## Prerequisites

- Go 1.26+
- Docker with Compose
- [Task](https://taskfile.dev) — `task setup` installs the rest (goose, ogen,
  sqlc, buf, golangci-lint)

Commands below are Bash (Git Bash on Windows). For PowerShell, replace
`export FOO=bar` with `$env:FOO = "bar"`.

## 1. Start the dev stack

```bash
task up
```

Brings up Postgres, Kafka, Redis, etcd, SeaweedFS, and the observability stack
(Tempo, Loki, Prometheus, Grafana, Pyroscope, promtail).

Wait until the containers report healthy:

```bash
docker compose -f deploy/compose/docker-compose.yml ps
```

Tempo takes ~10s after start to accept queries. Check it directly:

```bash
curl -s http://127.0.0.1:3200/ready   # prints "ready"
```

## 2. Apply migrations

```bash
export FLOWHAND_DB_DSN="postgres://flowhand:flowhand@127.0.0.1:5432/flowhand?sslmode=disable"
task migrate:up
```

Expected:

```
OK   00001_initial_tasks.sql
OK   00002_add_task_handler.sql
goose: successfully migrated database to version: 2
```

> Re-run this after any `task down`/`task up` cycle that resets the Postgres
> volume. A `500` with `relation "tasks" does not exist` means migrations are
> missing, not that the code is broken.

## 3. Run the server

In terminal A:

```bash
export FLOWHAND_OBS_OTLP_ENDPOINT=127.0.0.1:4317
export FLOWHAND_OBS_SERVICE_NAME=flowhand
export FLOWHAND_OBS_LOG_FORMAT=text
task run:server
```

Expected:

```
time=... level=INFO msg=listening addr=:8080
```

`FLOWHAND_OBS_LOG_FORMAT=text` is not cosmetic. Grafana's Loki datasource
extracts the trace ID with the regex `trace_id=(\w+)`, which matches slog's
text output. The `json` default emits `"trace_id":"..."` and the trace ↔ logs
correlation in step 7 will not link.

### Configuring by file instead of environment

Every setting above can also come from a YAML file. `configs/cfg.yaml` is a
worked example holding the two values you are most likely to change:

```bash
./bin/flowhand server --config configs/cfg.yaml
```

Settings are resolved in four layers, each overriding the one before it:
built-in defaults → config file → `FLOWHAND_*` environment → command-line
flags. So the file is a convenient baseline, and an env var or a flag still
wins over it — which is why `FLOWHAND_DB_DSN` in your shell beats the DSN
committed in the file, and `--log-level=debug` beats both.

## 4. Create a task

In terminal B:

```bash
curl -v -X POST http://127.0.0.1:8080/v1/tasks \
  -H 'content-type: application/json' \
  -d '{"handler":"echo","payload":{"msg":"hello"}}'
```

Expected `201 Created`:

```json
{"id":"019f7013-e758-77ce-9b37-9deb03430e1b","status":"pending","created_at":"2026-07-17T15:36:20+03:00"}
```

The `id` is a UUIDv7 — time-ordered, so `ORDER BY id` matches insertion order.

## 5. Verify the row landed

```bash
docker exec -e PGPASSWORD=flowhand flowhand-dev-postgres-1 \
  psql -U flowhand -d flowhand \
  -c "SELECT id, status, payload FROM tasks ORDER BY created_at DESC LIMIT 1;"
```

Expected:

```
                  id                  | status  |     payload
--------------------------------------+---------+------------------
 019f7013-e758-77ce-9b37-9deb03430e1b | pending | {"msg": "hello"}
```

If you have `psql` on your PATH, this works too:

```bash
PGPASSWORD=flowhand psql -h 127.0.0.1 -U flowhand -d flowhand \
  -c "SELECT id, status, payload FROM tasks ORDER BY created_at DESC LIMIT 1;"
```

## 6. Find the trace in Tempo

Open <http://127.0.0.1:3000/explore> → datasource **Tempo** → **Search** →
run the query with an empty filter `{}` → pick the most recent
`POST /v1/tasks` trace.

Expected span chain:

```
POST /v1/tasks                       (otelhttp)
└── createTask                       (internal/api)
    └── query -- name: CreateTask :one
        INSERT INTO tasks ...        (otelpgx)
```

`pool.acquire`, `connect`, and `prepare` spans from the pgx pool appear
alongside — that is normal.

To check from the terminal instead:

```bash
curl -s "http://127.0.0.1:3200/api/search/tag/service.name/values"
# {"tagValues":["flowhand"],...}
```

An empty `tagValues` means no spans reached Tempo at all — see troubleshooting.

## 7. Correlate the trace with its logs

The application side of this works: every log record emitted with a `*Context`
method carries the `trace_id` of its span, matching what Tempo recorded.

```
level=ERROR msg="create task failed" err="..." trace_id=77945d51... span_id=d018356e...
                                               ^^^^^^^^^^^^^^^^^^ same id as the Tempo trace
```

Two pieces make that happen:

- `internal/obs/log.go` wraps the slog handler to stamp `trace_id`/`span_id`
  from the span in the context. Only `*Context` methods (`InfoContext`,
  `ErrorContext`) carry a context, so plain `slog.Info` will not be linked.
- The Loki datasource's `derivedFields` regex in
  `deploy/compose/grafana/provisioning/datasources/datasources.yml` parses
  `trace_id=` out of the log line — which is why step 3 sets the text format.

The shipping side works too. `task run:server` tees the server's output into
`.logs/server.log`, which promtail tails through a bind mount and pushes to Loki
under `job="flowhand"`:

```
Taskfile run:server ──tee──▶ .logs/server.log
                                   │  (bind-mounted read-only at
                                   │   /var/log/flowhand in the promtail container)
                             promtail ──▶ Loki   {job="flowhand"}
```

Confirm the pipeline end to end — this should return your server's startup line:

```bash
curl -s -G "http://127.0.0.1:3100/loki/api/v1/query_range" \
  --data-urlencode 'query={job="flowhand"} |= "msg=listening"'
```

Then in Grafana: open the trace in Tempo and click **Logs for this span**. You
should land on the matching flowhand lines.

> **Why the tee, rather than just running the server.** flowhand is not
> containerised until Phase 4, so it runs on the *host*, while promtail only
> sees files. Writing to a directory promtail tails is the smallest bridge
> between the two. Phase 4 makes flowhand a compose service and the tee goes
> away.

> **If the jump still finds nothing**, check `FLOWHAND_OBS_LOG_FORMAT`. The Loki
> datasource carries two `derivedFields` matchers — `trace_id=(\w+)` for slog's
> text output and `"trace_id":"(\w+)"` for its JSON output — so either format
> links. A third format would not.

## Endpoints

| URL                                  | What                                |
| ------------------------------------ | ----------------------------------- |
| `http://127.0.0.1:8080/v1/tasks`     | API                                 |
| `http://127.0.0.1:8080/metrics`      | Prometheus metrics (OTel + runtime) |
| `http://127.0.0.1:8080/debug/pprof/` | pprof                               |
| `http://127.0.0.1:3000`              | Grafana                             |
| `http://127.0.0.1:3200`              | Tempo API                           |
| `http://127.0.0.1:9090/prometheus`   | Prometheus (served under a route prefix) |
| `http://127.0.0.1:9093`              | Alertmanager                        |
| `http://127.0.0.1:4040`              | Pyroscope                           |

Prometheus runs with `--web.route-prefix=/prometheus`, so every one of its
endpoints moves — `/prometheus/-/healthy`, `/prometheus/api/v1/targets`,
`/prometheus/metrics`. Bare `http://127.0.0.1:9090/` will 404.

## 8. Run the reference producer

`flowhand-demo` submits one task per second and exposes its own `/metrics`, so
Prometheus can chart submit throughput. It needs the stack up, the migrations
applied and the server running (steps 1–3) — without those it submits into a
void and the rate panel stays flat with no indication why.

```bash
task demo
```

Then in Grafana **Explore** (Prometheus datasource, not a dashboard —
dashboards are Phase 4):

```promql
rate(flowhand_demo_submits_total[1m])
```

Submits should appear within ~10 seconds. The `result` label separates
`created` from `replayed`: the producer submits every idempotency key twice and
checks that the second submit comes back with the *same* task ID, so a healthy
`replayed` rate is evidence the server's idempotency replay works — observed
from the response, not assumed.

```bash
docker compose -f deploy/compose/docker-compose.yml logs -f flowhand-demo
```

## Troubleshooting

**No traces in Tempo.** Confirm spans are arriving with the `service.name`
query in step 6. Metrics go to Prometheus by scrape, not to the OTLP endpoint —
only traces are exported to `:4317`. Tempo's OTLP receiver accepts traces only,
so pointing a metric exporter at `:4317` fails with `DeadlineExceeded`.

**`relation "tasks" does not exist`.** Re-run step 2.

**No jump-to-logs button, or it finds nothing.** The server's output reaches
Loki only through the tee in `task run:server`. Check, in order: `.logs/server.log`
exists and is growing; promtail is running (`docker compose ... ps promtail`);
and the Loki query in step 7 returns lines. Starting the binary directly
(`./bin/flowhand server`) instead of via `task run:server` skips the tee, which
is the usual cause.

**Prometheus URL 404s.** It is served under `/prometheus` — see the Endpoints
table.

**`task demo` fails to build.** It builds `cmd/flowhand-demo/Dockerfile` from
the *repo root* as context, so run it from anywhere in the repo but never with
a narrowed context; the module's `go.mod` has to be visible.

**Config env var seems ignored.** Only the first underscore separates section
from key: `FLOWHAND_OBS_LOG_FORMAT` → `obs.log_format`. Precedence is
defaults < config file < env < flags.

## Teardown

```bash
task down
```

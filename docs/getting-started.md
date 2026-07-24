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
goose: successfully migrated database to version: 1
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

> **Known gap — the Tempo → Loki jump does not work yet in this setup.**
> `task run:server` runs flowhand on the host, but promtail only tails
> container logs (`/var/lib/docker/containers/*/*-json.log`, see
> `deploy/compose/promtail/promtail.yml`). The server's stdout therefore never
> reaches Loki, and **Logs for this span** finds nothing to show.
>
> Verify for yourself — this returns only Loki's own query logs, never a
> flowhand line:
>
> ```bash
> curl -s -G "http://127.0.0.1:3100/loki/api/v1/query_range" \
>   --data-urlencode 'query={job="containerlogs"} |= "msg=listening"'
> ```
>
> Closing it needs one of: tee the server output to a host directory that
> promtail is configured to tail, run flowhand as a compose service, or push
> from the app with a Loki slog handler. Until then, correlate by copying the
> `trace_id` from the terminal and searching Tempo directly.

## Endpoints

| URL                                  | What                                |
| ------------------------------------ | ----------------------------------- |
| `http://127.0.0.1:8080/v1/tasks`     | API                                 |
| `http://127.0.0.1:8080/metrics`      | Prometheus metrics (OTel + runtime) |
| `http://127.0.0.1:8080/debug/pprof/` | pprof                               |
| `http://127.0.0.1:3000`              | Grafana                             |
| `http://127.0.0.1:3200`              | Tempo API                           |
| `http://127.0.0.1:9090`              | Prometheus                          |

## Troubleshooting

**No traces in Tempo.** Confirm spans are arriving with the `service.name`
query in step 6. Metrics go to Prometheus by scrape, not to the OTLP endpoint —
only traces are exported to `:4317`. Tempo's OTLP receiver accepts traces only,
so pointing a metric exporter at `:4317` fails with `DeadlineExceeded`.

**`relation "tasks" does not exist`.** Re-run step 2.

**No jump-to-logs button, or it finds nothing.** Expected for now — the server
runs on the host and promtail only tails container logs. See the known gap in
step 7. If you close that gap and it still fails, check
`FLOWHAND_OBS_LOG_FORMAT` is `text` (step 3), since the datasource regex only
matches the text format.

**Config env var seems ignored.** Only the first underscore separates section
from key: `FLOWHAND_OBS_LOG_FORMAT` → `obs.log_format`. Precedence is
defaults < config file < env < flags.

## Teardown

```bash
task down
```

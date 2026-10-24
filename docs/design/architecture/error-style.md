# Error style

flowhand uses the standard library's `errors` and `fmt` packages in every layer,
and no error framework. This page states the rules, shows where each kind of
error is created and where it changes shape, and points at the linters that keep
it that way.

The decision and its trade-offs are recorded in ADR 0006
(`adr/0006-stdlib-errors-no-error-framework.md`), which lives in the private
design vault rather than this repo.

## The rules

1. **An expected condition is a sentinel, owned by the layer that gives it
   meaning.** `domaintasks.ErrNotFound` and `ErrConflict` belong to the domain,
   because "no such task" and "this key is taken" are domain facts, even though
   a repository is what discovers them. A family of related failures gets an
   umbrella sentinel, and each specific reason wraps it:
   `ErrIdempotencyKeyTooLong` wraps `ErrInvalidIdempotencyKey`, so a caller can
   branch on either.

2. **An error that must carry data is a type that unwraps to its sentinel.**
   When the caller needs more than the class — the id of the task an
   idempotency conflict collided with, say — define a struct with an `Unwrap`
   method returning the sentinel. `errors.Is` still answers "which class?", and
   `errors.AsType[*T](err)` gets the data. Never make a caller parse a message
   string.

3. **Wrap with context, using `%w`.** `fmt.Errorf("get task %s: %w", id, err)`:
   lowercase, no trailing punctuation, saying what was being attempted. `%w` and
   never `%v` inside the codebase, so the chain stays inspectable all the way up.

4. **Translate at the boundary that owns the foreign error.** Errors from a
   dependency's vocabulary never cross a layer:
   - `internal/repository` turns driver errors into domain sentinels.
     `pgx.ErrNoRows` becomes `ErrNotFound`, and a unique violation (SQLSTATE
     23505) becomes `ErrConflict`. Nothing above the repository imports `pgx`,
     so nothing above it can see an SQLSTATE.
   - `internal/api` turns sentinels into HTTP responses. `ErrNotFound` becomes a
     404 with the `task_not_found` code; anything unrecognised is returned to
     ogen's `ErrorHandler`, which renders the `Error` envelope as a 500.

5. **Handle an error exactly once.** Either return it, wrapped, or handle it —
   log it, translate it, or deliberately swallow it — but never both log and
   return it. Handlers in `internal/api` log a failure once, at the transport
   boundary, before handing it to `ErrorHandler`, which only renders it. A soft
   failure that is swallowed on purpose, such as a Redis error on the
   idempotency fast path, which falls through to the database, is logged with
   `WarnContext` where it is swallowed. Otherwise a permanently broken
   dependency looks exactly like a healthy one.

6. **No panics for expected conditions.** A panic means a programmer error — a
   broken invariant — not a bad request, a missing row or a timeout.

## Why there is no error framework

Frameworks such as `samber/oops` offer codes, attributes and stack traces at the
error site. flowhand gets the code from sentinels and types, the attributes from
the `slog` call at the single place an error is handled, and the call path from
the `%w` chain and the trace span. The domain is a leaf that imports only the
standard library, and one style from the domain to the transport means no
boundary where errors change kind.

## What enforces it

- **`errorlint`** rejects `err == ErrX` and bare type assertions on errors. Use
  `errors.Is` and `errors.As` / `errors.AsType`, which see through wrapping.
- **`errcheck`**, with `check-blank: true`, rejects `_ = f()` for an error
  result. Even an error the contract says is always nil gets checked.
- **`depguard`** keeps `pgx` out of `internal/api` and `internal/service`, which
  is what makes rule 4's translation mandatory rather than polite. The layering
  itself is described in [`import-rules.md`](import-rules.md).

package outbox

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
	"github.com/RomanAgaltsev/flowhand/internal/repository"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

// Repo is the outbox repository. It resolves its DBTX per call so the same
// instance works inside a transaction or on the bare pool.
type Repo struct {
	resolver repository.Resolver
}

// New returns an outbox repository that resolves its connection through r.
func New(r repository.Resolver) *Repo {
	return &Repo{resolver: r}
}

// Append serialises each event into one outbox_events row and inserts them as one
// batch. No BeginTx: the resolver hands back the ambient transaction when the
// Commander is inside one, which is what makes the state change and its events atomic.
func (r *Repo) Append(ctx context.Context, events ...domain.Event) error {
	if len(events) == 0 {
		return nil // a no-op append is legal - a transition that emits nothing
	}

	q := queries.New(r.resolver.Resolve(ctx))
	rows := make([]queries.AppendOutboxBatchParams, 0, len(events))

	for _, ev := range events {
		envelope, err := encodeEnvelope(ev)
		if err != nil {
			// Name the event in the error. A failure here is a marshalling bug in one
			// event type and "json: unsupported type" without the name is unfindable.
			return fmt.Errorf("encode %s: %w", ev.EventName(), err)
		}
		rows = append(rows, queries.AppendOutboxBatchParams{
			EventID:     uuid.Must(uuid.NewV7()),
			AggregateID: ev.AggregateID(),
			Type:        ev.EventName(),
			EnvelopeVer: domain.EnvelopeVersion,
			TraceID:     traceIDFrom(ctx),
			Payload:     envelope,
		})
	}

	br := q.AppendOutboxBatch(ctx, rows)
	var firstErr error
	br.Exec(func(i int, err error) {
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("outbox event %d: %w", i, err)
		}
	})

	return firstErr
}

// traceIDFrom lifts the originating trace off ctx, if any. otel's trace
// package directly (a vendor dep, allowed by arch-lint) rather than obs: an
// internal/obs import would be an undeclared arch now and this is all obs's
// own handler does anyway.
func traceIDFrom(ctx context.Context) *string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	s := sc.TraceID().String()
	return &s
}

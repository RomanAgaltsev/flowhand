package repository

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// TraceID lifts the originating trace off ctx, or nil when there is none. It
// reads otel's trace package directly (a vendor dependency, which the layering
// rules allow) rather than internal/obs, which would be an undeclared edge in
// the import graph; this is all obs's own handler does anyway.
func TraceID(ctx context.Context) *string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	s := sc.TraceID().String()
	return &s
}

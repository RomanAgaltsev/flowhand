package tasks

import (
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

func toDomain(row queries.Task) domaintasks.Task {
	return domaintasks.FromPersistence(
		row.ID,
		domaintasks.Status(row.Status),
		row.Handler,
		row.Payload,
		row.CreatedAt,
	)
}

func toRow(t domaintasks.Task, idempotencyKey *string) queries.CreateTaskParams {
	return queries.CreateTaskParams{
		ID:             t.ID(),
		IdempotencyKey: idempotencyKey,
		Payload:        t.Payload(),
		Handler:        t.Handler(),
		// Status defaults to 'pending', CreatedAt to now() — see migration 00001.
	}
}

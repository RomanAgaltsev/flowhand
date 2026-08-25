package tasks

import (
	"github.com/google/uuid"

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

var defaultTenantID = uuid.Nil

func toRow(t domaintasks.Task, idempotencyKey *string) queries.CreateTaskParams {
	return queries.CreateTaskParams{
		ID:              t.ID(),
		IdempotencyKey:  idempotencyKey,
		Payload:         t.Payload(),
		Handler:         t.Handler(),
		TenantID:        defaultTenantID,
		Priority:        0,
		EarliestAt:      t.CreatedAt(),
		Attempt:         0,
		MaxAttempts:     25, // schema.md: per-HandlerSpec default
		ShardID:         0,
		CancelRequested: false,
		UpdatedAt:       t.CreatedAt(),
	}
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

// Querier is a subset of the sqlc-generated Queries - only what this handler needs.
type Querier interface {
	CreateTask(ctx context.Context, arg queries.CreateTaskParams) (queries.Task, error)
}

type Handler struct {
	q   Querier
	log *slog.Logger
}

func NewHandler(q Querier, log *slog.Logger) *Handler {
	return &Handler{
		q:   q,
		log: log,
	}
}

func (h *Handler) CreateTask(ctx context.Context, req *oas.CreateTaskRequest) (*oas.Task, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}

	payloadJSON := []byte("{}")
	if p, ok := req.Payload.Get(); ok {
		payloadJSON, err = json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
	}

	params := queries.CreateTaskParams{
		ID:             id,
		IdempotencyKey: optionalString(req.IdempotencyKey),
		Payload:        payloadJSON,
	}
	row, err := h.q.CreateTask(ctx, params)
	if err != nil {
		h.log.ErrorContext(ctx, "create task failed", "err", err)
		return nil, fmt.Errorf("insert task: %w", err)
	}

	return &oas.Task{
		ID:        row.ID,
		Status:    oas.TaskStatus(row.Status),
		CreatedAt: row.CreatedAt,
	}, nil
}

func optionalString(os oas.OptString) *string {
	if s, ok := os.Get(); ok {
		return &s
	}
	return nil
}

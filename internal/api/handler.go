package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

var tracer = otel.Tracer("github.com/RomanAgaltsev/flowhand/internal/api")

// Querier is a subset of the sqlc-generated Queries - only what this handler needs.
type Querier interface {
	CreateTask(ctx context.Context, arg queries.CreateTaskParams) (queries.Task, error)
	GetTaskByID(ctx context.Context, id uuid.UUID) (queries.Task, error)
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

func (h *Handler) CreateTask(ctx context.Context, req *oas.CreateTaskRequest) (oas.CreateTaskRes, error) {
	ctx, span := tracer.Start(ctx, "createTask")
	defer span.End()

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

	createTaskParams := queries.CreateTaskParams{
		ID:             id,
		IdempotencyKey: optionalString(req.IdempotencyKey),
		Payload:        payloadJSON,
	}
	row, err := h.q.CreateTask(ctx, createTaskParams)
	if err != nil {
		h.log.ErrorContext(ctx, "create task failed", "err", err)
		return &oas.CreateTaskInternalServerError{
			Code:    strconv.Itoa(http.StatusInternalServerError),
			Message: "internal error",
		}, nil
	}

	return &oas.Task{
		ID:        row.ID,
		Status:    oas.TaskStatus(row.Status),
		CreatedAt: row.CreatedAt,
	}, nil
}

func (h *Handler) GetTask(ctx context.Context, params oas.GetTaskParams) (oas.GetTaskRes, error) {
	ctx, span := tracer.Start(ctx, "getTask")
	defer span.End()

	row, err := h.q.GetTaskByID(ctx, params.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &oas.GetTaskNotFound{
				Code:    strconv.Itoa(http.StatusNotFound),
				Message: "task ID not found",
			}, nil
		}
		// Any other error (conn dead, timeout, cancel, pool-exhausted) is a real
		// failure. Returning it — not a zero-value Task — is the whole point; it
		// becomes a JSON 500 once getTask declares one.
		h.log.ErrorContext(ctx, "get task failed", "err", err)
		return &oas.GetTaskInternalServerError{
			Code:    strconv.Itoa(http.StatusInternalServerError),
			Message: "internal error",
		}, nil
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

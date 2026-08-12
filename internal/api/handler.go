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
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

var tracer = otel.Tracer("github.com/RomanAgaltsev/flowhand/internal/api")

// Querier is a subset of the sqlc-generated Queries - only what this handler needs.
type Querier interface {
	CreateTask(ctx context.Context, arg queries.CreateTaskParams) (queries.Task, error)
	GetTaskByID(ctx context.Context, id uuid.UUID) (queries.Task, error)
	GetTaskByIdempotencyKey(ctx context.Context, idempotencyKey *string) (queries.Task, error)
}

// Handler implements the ogen-generated server interface, backed by a Querier.
type Handler struct {
	q   Querier
	log *slog.Logger
}

// NewHandler returns a Handler that serves tasks from q and logs to log.
func NewHandler(q Querier, log *slog.Logger) *Handler {
	return &Handler{
		q:   q,
		log: log,
	}
}

// CreateTask persists a new task, replaying the existing one on an idempotency-key conflict.
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
		Handler:        req.Handler,
	}
	row, err := h.q.CreateTask(ctx, createTaskParams)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return h.replay(ctx, createTaskParams.IdempotencyKey)
		}
		h.log.ErrorContext(ctx, "create task failed", "err", err)
		return nil, fmt.Errorf("insert task: %w", err)
	}

	return &oas.Task{
		ID:        row.ID,
		Status:    oas.TaskStatus(row.Status),
		CreatedAt: row.CreatedAt,
	}, nil
}

// GetTask returns the task with the given ID, or a 404 when it does not exist.
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
		// failure. Returning it — not a typed 500 — lets ogen's ErrorHandler
		// render the Error envelope and marks the request as failed for tracing.
		h.log.ErrorContext(ctx, "get task failed", "err", err)
		return nil, fmt.Errorf("get task %s: %w", params.ID, err)
	}

	return &oas.Task{
		ID:        row.ID,
		Status:    oas.TaskStatus(row.Status),
		CreatedAt: row.CreatedAt,
	}, nil
}

// replay serves the task already stored under a conflicting idempotency key. A
// nil key or a failed lookup means the row we just collided with is unreadable,
// so both surface as an error for the ErrorHandler to render as a 500.
func (h *Handler) replay(ctx context.Context, key *string) (oas.CreateTaskRes, error) {
	if key == nil {
		return nil, errors.New("idempotency conflict without a key")
	}
	row, err := h.q.GetTaskByIdempotencyKey(ctx, key)
	if err != nil {
		h.log.ErrorContext(ctx, "idempotency replay failed", "err", err)
		return nil, fmt.Errorf("replay idempotent task: %w", err)
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

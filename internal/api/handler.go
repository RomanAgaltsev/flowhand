package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
)

var tracer = otel.Tracer("github.com/RomanAgaltsev/flowhand/internal/api")

// Commander is the write side this handler consumes. Declared here, not in the
// service package, so the handler owns the shape it depends on and tests can
// fake it. Satisfies by *task.Commander.
type Commander interface {
	Submit(ctx context.Context, cmd task.SubmitCommand) (domaintasks.Task, error)
}

// Querier is the read side. Satisfied by *task.Querier.
type Querier interface {
	Get(ctx context.Context, id uuid.UUID) (task.TaskView, error)
}

// Handler implements the ogen-generated server interface, backed by a Querier.
type Handler struct {
	cmd Commander
	q   Querier
	log *slog.Logger
}

// NewHandler returns a Handler that serves tasks from q and logs to log.
func NewHandler(cmd Commander, q Querier, log *slog.Logger) *Handler {
	return &Handler{cmd: cmd, q: q, log: log}
}

// CreateTask submits a new task. Idempotency replay is handled by the
// Commander, so a repeated key yields the original task, not a 409.
func (h *Handler) CreateTask(ctx context.Context, req *oas.CreateTaskRequest) (oas.CreateTaskRes, error) {
	ctx, span := tracer.Start(ctx, "createTask")
	defer span.End()

	payloadJSON := json.RawMessage("{}")
	if p, ok := req.Payload.Get(); ok {
		b, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
		payloadJSON = b
	}

	key, _ := req.IdempotencyKey.Get() // "" when absent - Commander treats that as no key

	t, err := h.cmd.Submit(ctx, task.SubmitCommand{
		Handler:        req.Handler,
		Payload:        payloadJSON,
		IdempotencyKey: key,
	})
	if err != nil {
		h.log.ErrorContext(ctx, "create task failed", "err", err)
		return nil, fmt.Errorf("submit task: %w", err)
	}

	dto, err := toOASTask(t)
	if err != nil {
		// An unmapped status is a contract violation on our side: 500, loudly.
		h.log.ErrorContext(ctx, "map task to response failed", "err", err)
		return nil, err
	}
	return &dto, nil
}

// GetTask returns the task with the given ID, or a 404 when it does not exist.
func (h *Handler) GetTask(ctx context.Context, params oas.GetTaskParams) (oas.GetTaskRes, error) {
	ctx, span := tracer.Start(ctx, "getTask")
	defer span.End()

	view, err := h.q.Get(ctx, params.ID)
	if err != nil {
		if errors.Is(err, domaintasks.ErrNotFound) {
			// Same symbol vocabulary as api.ErrorHandler: `code` is the stable
			// thing clients branch on, never a stringified HTTP status.
			return &oas.GetTaskNotFound{
				Code:    oas.ErrorCodeTaskNotFound,
				Message: "task ID not found",
			}, nil
		}
		// Anything else (conn dead, timeout, cancel, pool-exhausted) is a real
		// failure. Returning it - not a typed 500 - lets ogen's ErrorHandler
		// render the Error envelope and marks the request as failed for tracing.
		h.log.ErrorContext(ctx, "get task failed", "err", err)
		return nil, fmt.Errorf("get task %s: %w", params.ID, err)
	}

	dto, err := toOASTaskView(view)
	if err != nil {
		h.log.ErrorContext(ctx, "map task view to response failed", "err", err)
		return nil, err
	}
	return &dto, nil
}

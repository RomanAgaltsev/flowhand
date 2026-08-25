package api

import (
	"fmt"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
)

// toOASStatus converts a domain status to its published form.
//
// The two vocabularies are identical today, so every arm below is a rename of the
// same word. The function still earns its place twice over: its default arm is what
// turns an unmapped status into an error instead of an invalid response body, and it
// is the seam to widen if the wire ever needs to diverge for a real reason — say,
// reporting retry_scheduled as running rather than exposing scheduler internals.
//
// Divergence has to be earned by a difference in meaning. An earlier design spelled
// the same states differently on each side (complete/succeeded, canceled/cancelled),
// which bought nothing and cost a silent bug: the wire spelling leaked into a
// database trigger, where it could never match and so never fired.
func toOASStatus(s domaintasks.Status) (oas.TaskStatus, error) {
	switch s {
	case domaintasks.StatusPending:
		return oas.TaskStatusPending, nil
	case domaintasks.StatusRunning:
		return oas.TaskStatusRunning, nil
	case domaintasks.StatusRetryScheduled:
		return oas.TaskStatusRetryScheduled, nil
	case domaintasks.StatusSucceeded:
		return oas.TaskStatusSucceeded, nil
	case domaintasks.StatusFailed:
		return oas.TaskStatusFailed, nil
	case domaintasks.StatusCanceled:
		return oas.TaskStatusCanceled, nil
	case domaintasks.StatusDeadLettered:
		return oas.TaskStatusDeadLettered, nil
	default:
		return "", fmt.Errorf("unmapped task status %q", s)
	}
}

func toOASTask(t domaintasks.Task) (oas.Task, error) {
	status, err := toOASStatus(t.Status())
	if err != nil {
		return oas.Task{}, err
	}
	return oas.Task{ID: t.ID(), Status: status, CreatedAt: t.CreatedAt()}, nil
}

// toOASTaskView maps the read-side projection. TaskView.Status is a plain
// string by design (the read model stays free of domain types), so the domain
// conversion happens here, at the boundary that owns vocabulary mismatches.
func toOASTaskView(v task.TaskView) (oas.Task, error) {
	status, err := toOASStatus(domaintasks.Status(v.Status))
	if err != nil {
		return oas.Task{}, err
	}
	return oas.Task{ID: v.ID, Status: status, CreatedAt: v.CreatedAt}, nil
}

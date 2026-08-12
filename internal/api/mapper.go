package api

import (
	"fmt"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

func toOASStatus(s domaintasks.Status) (oas.TaskStatus, error) {
	switch s {
	case domaintasks.StatusPending:
		return oas.TaskStatusPending, nil
	case domaintasks.StatusRunning:
		return oas.TaskStatusRunning, nil
	case domaintasks.StatusComplete:
		return oas.TaskStatusSucceeded, nil
	case domaintasks.StatusFailed:
		return oas.TaskStatusFailed, nil
	case domaintasks.StatusCanceled:
		return oas.TaskStatusCancelled, nil
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

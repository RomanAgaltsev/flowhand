package tasks_test

import (
	repotasks "github.com/RomanAgaltsev/flowhand/internal/repository/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
)

var _ task.TasksRepo = (*repotasks.Repo)(nil)
var _ task.AttemptsRepo = (*repotasks.Repo)(nil) // same concrete type, second port

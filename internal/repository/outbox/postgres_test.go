package outbox_test

import (
	repoutbox "github.com/RomanAgaltsev/flowhand/internal/repository/outbox"
	repotask "github.com/RomanAgaltsev/flowhand/internal/service/task"
)

var _ repotask.OutboxRepo = (*repoutbox.Repo)(nil)

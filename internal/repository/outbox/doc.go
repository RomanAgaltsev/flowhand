// Package outbox persists domain events in the same transaction as the state
// change that produced them - the transactional-outbox pattern.
//
// Phase 0 ships a no-op; Phase 1 writes real outbox_events rows.
//
// Layer: persistence. Same import rules as internal/repository/tasks.
package outbox

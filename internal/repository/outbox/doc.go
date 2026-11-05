// Package outbox persists domain events in the same transaction as the state
// change that produced them - the transactional-outbox pattern.
//
// Layer: persistence. Same import rules as internal/repository/tasks.
package outbox

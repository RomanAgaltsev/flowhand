// Package schedules will be the schedules bounded context (Phase 1 cron),
// owning the Schedule aggregate and its events.
//
// Layer: domain. It MUST NOT import a sibling context such as
// internal/domain/tasks, nor any outer layer. It MAY import internal/domain
// for the shared Event contract.
package schedules

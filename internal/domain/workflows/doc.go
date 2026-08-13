// Package workflows will be the workflows bounded context (Phase 2), owning
// the WorkflowRun aggregate and its events.
//
// Layer: domain. It MUST NOT import a sibling context such as
// internal/domain/tasks, nor any outer layer. It MAY import internal/domain
// for the shared Event contract.
package workflows

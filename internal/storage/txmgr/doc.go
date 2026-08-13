// Package txmgr owns transaction scope. It satisfies both
// repository.Resolver (which DBTX is in play) and service/task.TxRunner
// (run this closure inside a transaction), which is what lets the service
// layer say "these writes are atomic" without naming pgx.
//
// Phase 0 ships a non-transactional stub; Phase 1 puts a real pgx.Tx in the
// context.
//
// Layer: infrastructure. It MAY import internal/repository. It MUST NOT
// import internal/service/* or internal/api/*.
package txmgr

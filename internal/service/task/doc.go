// Package task is the service layer for the tasks bounded context. It holds
// the use cases - Commander for writes, Querier for reads - and declares the
// ports it needs in ports.go, which the persistence layer satisfies.
//
// Layer: service. It MAY import internal/domain/*, internal/repository (for
// the ports' types), and internal/client. It MUST NOT import pgx, sqlc
// (internal/storage/queries), or internal/api/*.
package task

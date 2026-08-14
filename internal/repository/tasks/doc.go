// Package tasks is the Postgres-backed repository for the tasks bounded
// context. It maps sqlc rows to domain aggregates and translates driver
// errors into domain sentinels.
//
// Layer: persistence. It MAY import internal/domain/*, internal/repository,
// and internal/storage/queries. It MUST NOT import internal/service/* or
// internal/api/*.
package tasks

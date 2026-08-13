// Package repository defines the storage seam the concrete repositories sit
// on: DBTX (the surface sqlc-generated queries accept) and Resolver (which
// hands back the transaction bound to a context, or the pool when there is
// none).
//
// Layer: persistence. It and its sub-packages are - with internal/storage -
// the only place allowed to import pgx. Everything above them sees domain
// sentinels such as tasks.ErrNotFound, never SQLSTATE codes or driver types.
package repository

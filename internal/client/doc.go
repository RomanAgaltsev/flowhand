// Package client will hold outbound adapters to systems that are not the
// primary database - Redis, Kafka, etcd, S3-compatible object storage.
// Placeholder until Phase 1.
//
// Layer: infrastructure, consumed by the service layer through ports. It MAY
// import internal/domain/*. It MUST NOT import internal/service/* or
// internal/api/*.
package client

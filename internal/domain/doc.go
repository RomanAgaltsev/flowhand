// Package domain holds the vocabulary shared by every bounded context: the
// Event contract aggregates emit, and the envelope version that wraps it on
// the wire.
//
// Layer: domain (innermost). It MUST NOT import internal/repository/*,
// internal/service/*, internal/api/*, internal/storage/*, or internal/client.
// It MAY import the standard library and small generic helpers.
package domain

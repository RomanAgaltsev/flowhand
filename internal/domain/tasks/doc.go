// Package tasks is the tasks bounded context. It owns the Task aggregate
// root, its value objects, and the events it emits. It MUST NOT import
// any other internal/domain/* sub-package, internal/repository/*,
// internal/service/*, or internal/api/*. It MAY import standard library
// and small generic helpers.
package tasks

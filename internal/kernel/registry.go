package kernel

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// port is the constraint every port interface satisfies: an adapter is addressable by a stable
// string ID, and that is what the registries key on.
type port interface {
	ID() string
}

// registry is a typed port registry. One instance exists per port kind, so a Sandbox can never be
// looked up as a ForgeProvider and duplicate detection is per kind rather than global.
type registry[T port] struct {
	kind string

	mu      sync.RWMutex
	entries map[string]registration[T]
}

type registration[T port] struct {
	value  T
	module string
}

func newRegistry[T port](kind string) *registry[T] {
	return &registry[T]{kind: kind, entries: make(map[string]registration[T])}
}

// register adds v under its own ID, attributing it to the module that registered it. A duplicate
// ID is a DuplicateIDError naming both modules; an empty ID is a programming error in the adapter
// and is reported as one.
func (r *registry[T]) register(module string, v T) error {
	id := v.ID()
	if id == "" {
		return fmt.Errorf("module %q registered a %s with an empty ID; every port needs a stable ID", module, r.kind)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.entries[id]; ok {
		return &DuplicateIDError{Port: r.kind, ID: id, Module: module, ExistingModule: existing.module}
	}
	r.entries[id] = registration[T]{value: v, module: module}
	return nil
}

// get returns the adapter with this ID, or a NotFoundError naming the ID that was asked for.
func (r *registry[T]) get(id string) (T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.entries[id]; ok {
		return e.value, nil
	}
	var zero T
	return zero, &NotFoundError{Port: r.kind, ID: id, Known: r.idsLocked()}
}

// all returns every registered adapter, ordered by ID. The order is by ID rather than by
// registration so that it does not depend on the order of lines in cmd/lexicode: two runs of the
// same binary, and a test that registers in a different order, all see the same sequence.
func (r *registry[T]) all() []T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]T, 0, len(r.entries))
	for _, id := range r.idsLocked() {
		out = append(out, r.entries[id].value)
	}
	return out
}

// idsLocked returns the sorted IDs. The caller must hold at least the read lock.
func (r *registry[T]) idsLocked() []string {
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// DuplicateIDError is returned when two adapters claim the same port ID. It names the port kind,
// the ID and both modules, because "duplicate ID github" without the two module names sends the
// reader looking through every module in the binary.
type DuplicateIDError struct {
	Port           string // "forge", "sandbox", …
	ID             string
	Module         string // the module that was registering when the clash was detected
	ExistingModule string // the module that registered the ID first
}

func (e *DuplicateIDError) Error() string {
	return fmt.Sprintf(
		"duplicate %s port ID %q: module %q already registered it and module %q registered it again; give one of them a different ID",
		e.Port, e.ID, e.ExistingModule, e.Module)
}

// ErrDuplicateID matches any DuplicateIDError under errors.Is.
var ErrDuplicateID = errors.New("duplicate port ID")

func (e *DuplicateIDError) Is(target error) bool { return target == ErrDuplicateID }

// NotFoundError is the typed not-found every single-instance port lookup returns. It names the ID
// that was asked for, and the IDs that do exist, because the usual cause is a typo in stored
// configuration.
type NotFoundError struct {
	Port  string
	ID    string
	Known []string
}

func (e *NotFoundError) Error() string {
	if len(e.Known) == 0 {
		return fmt.Sprintf("no %s port with ID %q is registered; no %s is registered at all", e.Port, e.ID, e.Port)
	}
	return fmt.Sprintf("no %s port with ID %q is registered; registered: %s",
		e.Port, e.ID, join(e.Known))
}

// ErrNotFound matches any NotFoundError under errors.Is.
var ErrNotFound = errors.New("port not found")

func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

func join(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ", "
		}
		out += `"` + id + `"`
	}
	return out
}

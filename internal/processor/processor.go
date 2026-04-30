// Package processor defines a strategy interface for streaming records
// out of a single source file (CSV today, JSON/Parquet tomorrow) and
// a registry that maps a name or extension to a concrete Processor.
package processor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// Record carries either a parsed user or a parse error from a Processor.
type Record struct {
	User user.User
	Err  error
}

// Processor reads one source file and emits Records on a channel.
// The channel is closed when the file is exhausted or ctx is done.
type Processor interface {
	Name() string
	Extensions() []string
	Stream(ctx context.Context, path string) (<-chan Record, error)
}

// Registry resolves Processors by logical name (the -format flag) or by
// file extension (used during folder discovery).
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Processor
	byExt  map[string]Processor
}

func NewRegistry() *Registry {
	return &Registry{
		byName: make(map[string]Processor),
		byExt:  make(map[string]Processor),
	}
}

func (r *Registry) Register(p Processor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[p.Name()] = p
	for _, e := range p.Extensions() {
		r.byExt[strings.ToLower(e)] = p
	}
}

func (r *Registry) Lookup(name string) (Processor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("processor %q not registered", name)
	}
	return p, nil
}

func (r *Registry) ForExt(path string) (Processor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byExt[strings.ToLower(filepath.Ext(path))]
	return p, ok
}

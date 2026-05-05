package metrics

import "sync/atomic"

// Metrics is a thread-safe counter collector. Today only UsersAdded
// is exposed, but the type can grow to cover request rates, error
// rates, etc. without breaking callers.
type Metrics struct {
	usersAdded atomic.Int64
}

func New() *Metrics {
	return &Metrics{}
}

func (m *Metrics) IncUserAdded() {
	m.usersAdded.Add(1)
}

// UsersAdded returns the total number of users created since process
// start. Safe for concurrent reads.
func (m *Metrics) UsersAdded() int64 {
	return m.usersAdded.Load()
}

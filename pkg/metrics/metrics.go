package metrics

// Metrics is a lightweight collector for application metrics.
type Metrics struct {
	UsersAdded int
}

func New() *Metrics {
	return &Metrics{}
}

func (m *Metrics) IncUserAdded() {
	m.UsersAdded++
}

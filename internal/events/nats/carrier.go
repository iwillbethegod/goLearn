package nats

import (
	natsgo "github.com/nats-io/nats.go"
)

// HeaderCarrier adapts nats.Header to OpenTelemetry's
// propagation.TextMapCarrier so the W3C TraceContext + Baggage
// propagators can inject and extract span context across the broker.
type HeaderCarrier natsgo.Header

// Get returns the first value for the key (TextMapCarrier contract:
// single-value lookup; multi-value isn't used by W3C TraceContext).
func (c HeaderCarrier) Get(key string) string {
	if c == nil {
		return ""
	}
	return natsgo.Header(c).Get(key)
}

// Set replaces all existing values for key with value.
func (c HeaderCarrier) Set(key string, value string) {
	natsgo.Header(c).Set(key, value)
}

// Keys returns the list of header names. Order is unspecified.
func (c HeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

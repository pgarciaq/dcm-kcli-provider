package events

import "github.com/nats-io/nats.go"

// NewNATSPublisherForTest builds a NATSPublisher with an explicit connection (or nil).
// It exists only for tests in package events_test.
func NewNATSPublisherForTest(conn *nats.Conn) *NATSPublisher {
	return &NATSPublisher{conn: conn}
}

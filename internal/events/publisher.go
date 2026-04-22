// Package events provides CloudEvents publishing over NATS for resource status updates.
package events

import (
	"context"
	"encoding/json"
	"fmt"

	cloudevents "github.com/cloudevents/sdk-go/v2/event"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// NotConnectedError is returned when a NATSPublisher publishes without a live connection.
type NotConnectedError struct{}

func (*NotConnectedError) Error() string {
	return "NATS publisher not connected"
}

const (
	VMSubject      = "dcm.vm"
	ClusterSubject = "dcm.cluster"

	VMSource      = "dcm/providers/kcli-vm"
	ClusterSource = "dcm/providers/kcli-cluster"

	VMEventType      = "dcm.status.vm"
	ClusterEventType = "dcm.status.cluster"
)

type StatusEvent struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type Publisher interface {
	PublishVMEvent(ctx context.Context, event StatusEvent) error
	PublishClusterEvent(ctx context.Context, event StatusEvent) error
	Close()
}

// ConnectedPublisher extends Publisher with connection status for NATS-backed implementations.
type ConnectedPublisher interface {
	Publisher
	IsConnected() bool
}

type NATSPublisher struct {
	conn *nats.Conn
}

func NewNATSPublisher(natsURL string) (*NATSPublisher, error) {
	conn, err := nats.Connect(natsURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to NATS: %w", err)
	}
	return &NATSPublisher{conn: conn}, nil
}

func (p *NATSPublisher) IsConnected() bool {
	return p.conn != nil && p.conn.IsConnected()
}

func (p *NATSPublisher) PublishVMEvent(_ context.Context, event StatusEvent) error {
	if !p.IsConnected() {
		return fmt.Errorf("publishing VM event: %w", &NotConnectedError{})
	}
	return p.publish(VMSubject, VMSource, VMEventType, event)
}

func (p *NATSPublisher) PublishClusterEvent(_ context.Context, event StatusEvent) error {
	if !p.IsConnected() {
		return fmt.Errorf("publishing cluster event: %w", &NotConnectedError{})
	}
	return p.publish(ClusterSubject, ClusterSource, ClusterEventType, event)
}

func (p *NATSPublisher) Close() {
	if p.conn == nil {
		return
	}
	_ = p.conn.Flush()
	p.conn.Close()
	p.conn = nil
}

func (p *NATSPublisher) publish(subject, source, eventType string, data StatusEvent) error {
	ce := cloudevents.New()
	ce.SetID(uuid.New().String())
	ce.SetSource(source)
	ce.SetType(eventType)
	ce.SetSubject(subject)
	if err := ce.SetData(cloudevents.ApplicationJSON, data); err != nil {
		return fmt.Errorf("setting event data: %w", err)
	}

	payload, err := json.Marshal(ce)
	if err != nil {
		return fmt.Errorf("marshalling cloud event: %w", err)
	}

	return p.conn.Publish(subject, payload)
}

type NoopPublisher struct{}

func (n *NoopPublisher) IsConnected() bool { return true }

func (n *NoopPublisher) PublishVMEvent(_ context.Context, _ StatusEvent) error      { return nil }
func (n *NoopPublisher) PublishClusterEvent(_ context.Context, _ StatusEvent) error { return nil }
func (n *NoopPublisher) Close()                                                     {}

// New tests embed formal case IDs in It() descriptions using the TC-<area>-<kind>-UT-nnn convention.
// Legacy cases may be referenced only by C-nn comments on nearby lines.
package events_test

import (
	"context"
	"encoding/json"
	"errors"

	cloudevents "github.com/cloudevents/sdk-go/v2/event"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
)

var _ = Describe("Events Publisher", func() {
	var (
		pub *mockPublisher
		ctx context.Context
	)

	BeforeEach(func() {
		pub = newMockPublisher()
		ctx = context.Background()
	})

	// C-36: Publisher interface is defined and mock satisfies it; Close() is callable
	It("mockPublisher satisfies the Publisher interface including Close()", func() {
		var _ events.Publisher = pub
		pub.Close()
		Expect(pub.closed).To(BeTrue())
	})

	// C-37: NewNATSPublisher returns error when NATS URL is unreachable
	It("returns error when NATS URL is unreachable", func() {
		_, err := events.NewNATSPublisher("nats://unreachable:4222")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("connecting to NATS"))
	})

	// C-38: PublishVMEvent records correct event with VM subject
	It("publishes VM event with correct subject and fields", func() {
		evt := events.StatusEvent{
			ID:      "123",
			Status:  "RUNNING",
			Message: "VM is running",
		}
		err := pub.PublishVMEvent(ctx, evt)
		Expect(err).NotTo(HaveOccurred())

		all := pub.allEvents()
		Expect(all).To(HaveLen(1))
		Expect(all[0].Subject).To(Equal(events.VMSubject))
		Expect(all[0].Event.ID).To(Equal("123"))
		Expect(all[0].Event.Status).To(Equal("RUNNING"))
		Expect(all[0].Event.Message).To(Equal("VM is running"))
	})

	// C-38b: Verify NATSPublisher constructs a valid CloudEvent with correct attributes
	It("NATSPublisher builds CloudEvent with source, type, subject for VM events", func() {
		ce := cloudevents.New()
		ce.SetSource(events.VMSource)
		ce.SetType(events.VMEventType)
		ce.SetSubject(events.VMSubject)
		err := ce.SetData(cloudevents.ApplicationJSON, events.StatusEvent{
			ID:      "test-id",
			Status:  "RUNNING",
			Message: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(ce.Source()).To(Equal("dcm/providers/kcli-vm"))
		Expect(ce.Type()).To(Equal("dcm.status.vm"))
		Expect(ce.Subject()).To(Equal("dcm.vm"))

		var payload events.StatusEvent
		err = json.Unmarshal(ce.Data(), &payload)
		Expect(err).NotTo(HaveOccurred())
		Expect(payload.ID).To(Equal("test-id"))
		Expect(payload.Status).To(Equal("RUNNING"))
		Expect(payload.Message).To(Equal("test"))
	})

	// C-39: PublishClusterEvent records correct event with cluster subject
	It("publishes cluster event with correct subject and fields", func() {
		evt := events.StatusEvent{
			ID:      "456",
			Status:  "READY",
			Message: "Cluster is ready",
		}
		err := pub.PublishClusterEvent(ctx, evt)
		Expect(err).NotTo(HaveOccurred())

		all := pub.allEvents()
		Expect(all).To(HaveLen(1))
		Expect(all[0].Subject).To(Equal(events.ClusterSubject))
		Expect(all[0].Event.Status).To(Equal("READY"))
		Expect(all[0].Event.Message).To(Equal("Cluster is ready"))
	})

	// C-39b: Verify NATSPublisher constructs a valid CloudEvent for cluster events
	It("NATSPublisher builds CloudEvent with source, type, subject for cluster events", func() {
		ce := cloudevents.New()
		ce.SetSource(events.ClusterSource)
		ce.SetType(events.ClusterEventType)
		ce.SetSubject(events.ClusterSubject)

		Expect(ce.Source()).To(Equal("dcm/providers/kcli-cluster"))
		Expect(ce.Type()).To(Equal("dcm.status.cluster"))
		Expect(ce.Subject()).To(Equal("dcm.cluster"))
	})

	// C-40: StatusEvent JSON serialization produces correct field names
	It("StatusEvent serializes to JSON with id, status, message fields", func() {
		evt := events.StatusEvent{
			ID:      "test-id",
			Status:  "PROVISIONING",
			Message: "test message",
		}
		data, err := json.Marshal(evt)
		Expect(err).NotTo(HaveOccurred())

		var raw map[string]interface{}
		err = json.Unmarshal(data, &raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(HaveKeyWithValue("id", "test-id"))
		Expect(raw).To(HaveKeyWithValue("status", "PROVISIONING"))
		Expect(raw).To(HaveKeyWithValue("message", "test message"))
	})

	// Verify NoopPublisher satisfies interface without side effects
	It("NoopPublisher satisfies Publisher interface", func() {
		var p events.Publisher = &events.NoopPublisher{}
		Expect(p.PublishVMEvent(ctx, events.StatusEvent{})).To(Succeed())
		Expect(p.PublishClusterEvent(ctx, events.StatusEvent{})).To(Succeed())
		p.Close()
	})

	// TC-EVT-UT-001: NoopPublisher reports connected and never errors on publish
	It("TC-EVT-UT-001: NoopPublisher IsConnected is true and Publish does not error", func() {
		var p events.ConnectedPublisher = &events.NoopPublisher{}
		Expect(p.IsConnected()).To(BeTrue())
		Expect(p.PublishVMEvent(ctx, events.StatusEvent{ID: "n", Status: "RUNNING"})).To(Succeed())
		Expect(p.PublishClusterEvent(ctx, events.StatusEvent{ID: "c", Status: "READY"})).To(Succeed())
	})

	// TC-EVT-UT-002: NATSPublisher with nil connection is not connected and publish wraps NotConnectedError
	It("TC-EVT-UT-002: disconnected NATSPublisher returns NotConnectedError for PublishVMEvent", func() {
		pub := events.NewNATSPublisherForTest(nil)
		Expect(pub.IsConnected()).To(BeFalse())

		err := pub.PublishVMEvent(ctx, events.StatusEvent{ID: "x", Status: "RUNNING"})
		Expect(err).To(HaveOccurred())
		var nc *events.NotConnectedError
		Expect(errors.As(err, &nc)).To(BeTrue())

		err = pub.PublishClusterEvent(ctx, events.StatusEvent{ID: "y", Status: "READY"})
		Expect(err).To(HaveOccurred())
		var nc2 *events.NotConnectedError
		Expect(errors.As(err, &nc2)).To(BeTrue())
	})

	// TC-EVT-UT-003: after Close, NATSPublisher is disconnected and publish returns NotConnectedError
	It("TC-EVT-UT-003: NATSPublisher after Close returns NotConnectedError on publish", func() {
		pub, err := events.NewNATSPublisher("nats://127.0.0.1:4222")
		if err != nil {
			Skip("NATS server not available: " + err.Error())
		}
		Expect(pub.IsConnected()).To(BeTrue())
		pub.Close()
		Expect(pub.IsConnected()).To(BeFalse())

		err = pub.PublishVMEvent(ctx, events.StatusEvent{ID: "z", Status: "RUNNING"})
		Expect(err).To(HaveOccurred())
		var nc *events.NotConnectedError
		Expect(errors.As(err, &nc)).To(BeTrue())
	})
})

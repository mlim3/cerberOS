package bus

import (
	"context"
	"sync"
	"testing"
	"time"

	"aegis-databus/pkg/envelope"

	"github.com/nats-io/nats.go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

func addTestStream(t *testing.T, js nats.JetStreamContext, name, subj string) {
	t.Helper()
	_, err := js.AddStream(&nats.StreamConfig{
		Name:     name,
		Subjects: []string{subj},
		Storage:  nats.FileStorage,
		MaxAge:   24 * time.Hour,
	})
	if err != nil && err.Error() != "stream name already in use" {
		t.Fatalf("add stream: %v", err)
	}
}

func TestPublishAsync(t *testing.T) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		t.Skipf("NATS: %v", err)
	}
	defer container.Terminate(ctx)
	uri, _ := container.ConnectionString(ctx)
	nc, _ := nats.Connect(uri)
	defer nc.Close()
	js, _ := nc.JetStream()
	addTestStream(t, js, "TEST_ASYNC", "aegis.tasks.>")

	ev := envelope.Build("aegis/test", "aegis.tasks.async", map[string]string{"n": "1"})
	payload := ev.MustMarshal()

	var wg sync.WaitGroup
	var gotAck *nats.PubAck
	var gotErr error
	wg.Add(1)
	PublishAsync(js, "aegis.tasks.async", payload, func(ack *nats.PubAck, err error) {
		gotAck, gotErr = ack, err
		wg.Done()
	})
	wg.Wait()
	if gotErr != nil {
		t.Fatalf("PublishAsync: %v", gotErr)
	}
	if gotAck == nil {
		t.Fatal("expected ack")
	}
}

func TestPublishBatch(t *testing.T) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		t.Skipf("NATS: %v", err)
	}
	defer container.Terminate(ctx)
	uri, _ := container.ConnectionString(ctx)
	nc, _ := nats.Connect(uri)
	defer nc.Close()
	js, _ := nc.JetStream()
	addTestStream(t, js, "TEST_BATCH", "aegis.tasks.>")

	msgs := []BatchMessage{
		{Subject: "aegis.tasks.batch", Payload: envelope.Build("aegis/test", "aegis.tasks.batch", map[string]int{"n": 1}).MustMarshal()},
		{Subject: "aegis.tasks.batch", Payload: envelope.Build("aegis/test", "aegis.tasks.batch", map[string]int{"n": 2}).MustMarshal()},
		{Subject: "aegis.tasks.batch", Payload: envelope.Build("aegis/test", "aegis.tasks.batch", map[string]int{"n": 3}).MustMarshal()},
	}
	res := PublishBatch(js, msgs)
	if res.Err != nil {
		t.Fatalf("PublishBatch: %v", res.Err)
	}
	if len(res.Acks) != 3 {
		t.Errorf("expected 3 acks, got %d", len(res.Acks))
	}
}

func TestPublishBatch_ValidationError(t *testing.T) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		t.Skipf("NATS: %v", err)
	}
	defer container.Terminate(ctx)
	uri, _ := container.ConnectionString(ctx)
	nc, _ := nats.Connect(uri)
	defer nc.Close()
	js, _ := nc.JetStream()
	addTestStream(t, js, "TEST_BATCH_ERR", "aegis.tasks.>")

	msgs := []BatchMessage{
		{Subject: "aegis.tasks.batch", Payload: envelope.Build("aegis/test", "aegis.tasks.batch", map[string]int{"n": 1}).MustMarshal()},
		{Subject: "aegis.tasks.batch", Payload: []byte(`{"invalid": "no id"}`)},
		{Subject: "aegis.tasks.batch", Payload: envelope.Build("aegis/test", "aegis.tasks.batch", map[string]int{"n": 3}).MustMarshal()},
	}
	res := PublishBatch(js, msgs)
	if res.Err == nil {
		t.Error("expected validation error")
	}
	if len(res.Acks) != 2 {
		t.Errorf("expected 2 acks (one failed), got %d", len(res.Acks))
	}
}

func TestSubscribeWithACL_DLQDenied(t *testing.T) {
	// SR-DB-006: non-admin cannot subscribe to aegis.dlq
	// No NATS needed — ACL check fails before subscribe
	_, err := SubscribeWithACL(nil, "aegis-demo", SubjectDLQ, func(*nats.Msg) {})
	if err == nil {
		t.Fatal("expected ErrACLDenied for aegis-demo subscribing to aegis.dlq")
	}
}

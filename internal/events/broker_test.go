package events

import (
	"testing"
	"time"
)

func TestPublishAndUnsubscribe(t *testing.T) {
	b := New()
	ch, unsubscribe := b.Subscribe()
	b.Publish(Event{Type: "test", Data: map[string]string{"status": "ok"}})
	select {
	case payload := <-ch:
		if len(payload) == 0 { t.Fatal("empty payload") }
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
	unsubscribe()
	if _, open := <-ch; open { t.Fatal("subscriber channel is still open") }
}

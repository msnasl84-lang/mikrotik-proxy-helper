package events

import (
	"encoding/json"
	"sync"
)

type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type Broker struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]chan []byte
}

func New() *Broker { return &Broker{subscribers: make(map[uint64]chan []byte)} }

func (b *Broker) Subscribe() (<-chan []byte, func()) {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	ch := make(chan []byte, 32)
	b.subscribers[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, id)
			close(ch)
			b.mu.Unlock()
		})
	}
}

func (b *Broker) Publish(event Event) {
	payload, err := json.Marshal(event)
	if err != nil { return }
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subscribers {
		select { case ch <- payload: default: }
	}
}

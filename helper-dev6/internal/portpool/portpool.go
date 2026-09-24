package portpool

import (
	"errors"
	"fmt"
	"net"
	"sync"
)

var ErrExhausted = errors.New("test port pool exhausted")

type Pool struct {
	mu   sync.Mutex
	min  int
	max  int
	used map[int]struct{}
}

func New(min, max int) (*Pool, error) {
	if min < 1024 || max > 65535 || min > max {
		return nil, fmt.Errorf("invalid port range %d-%d", min, max)
	}
	return &Pool{min: min, max: max, used: make(map[int]struct{})}, nil
}

func (p *Pool) Acquire() (int, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for port := p.min; port <= p.max; port++ {
		if _, exists := p.used[port]; exists {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		p.used[port] = struct{}{}
		var once sync.Once
		return port, func() { once.Do(func() { p.release(port) }) }, nil
	}
	return 0, nil, ErrExhausted
}

func (p *Pool) release(port int) {
	p.mu.Lock()
	delete(p.used, port)
	p.mu.Unlock()
}

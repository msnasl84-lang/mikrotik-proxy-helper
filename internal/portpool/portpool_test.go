package portpool

import "testing"

func TestAcquireRelease(t *testing.T) {
	p, err := New(32100, 32101)
	if err != nil { t.Fatal(err) }
	a, releaseA, err := p.Acquire()
	if err != nil { t.Fatal(err) }
	b, releaseB, err := p.Acquire()
	if err != nil { t.Fatal(err) }
	if a == b { t.Fatalf("duplicate port %d", a) }
	if _, _, err := p.Acquire(); err != ErrExhausted { t.Fatalf("got %v, want ErrExhausted", err) }
	releaseA()
	releaseA()
	c, releaseC, err := p.Acquire()
	if err != nil { t.Fatal(err) }
	if c != a { t.Fatalf("got %d, want released port %d", c, a) }
	releaseB(); releaseC()
}

func TestRejectInvalidRange(t *testing.T) {
	if _, err := New(20000, 10000); err == nil { t.Fatal("expected invalid range error") }
}

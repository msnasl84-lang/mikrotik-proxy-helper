package testengine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body, ip, country string
		bad                     bool
	}{
		{"ipv4", "fl=abc\nip=203.0.113.8\nloc=DE\n", "203.0.113.8", "DE", false},
		{"ipv6", "ip=2001:db8::1\r\nloc=NL\r\n", "2001:db8::1", "NL", false},
		{"html", "<html>blocked</html>", "", "", true},
		{"bad IP", "ip=not-an-ip\nloc=DE", "", "", true},
		{"missing country", "ip=203.0.113.8", "203.0.113.8", "", true},
		{"unknown country", "ip=203.0.113.8\nloc=XX", "203.0.113.8", "", true},
		{"duplicate", "ip=203.0.113.8\nip=203.0.113.9\nloc=DE", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ip, country, err := parseIdentity([]byte(tc.body))
			if (err != nil) != tc.bad || ip != tc.ip || country != tc.country {
				t.Fatalf("got %q %q %v", ip, country, err)
			}
		})
	}
}

// The target does not resolve locally. Only the specified SOCKS listener can
// supply this response, so these checks catch host-network/active-tunnel leaks.
func identitySOCKS(t *testing.T, body string) (int, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	seen := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		hello := make([]byte, 3)
		if _, err := io.ReadFull(conn, hello); err != nil {
			return
		}
		if string(hello) != string([]byte{5, 1, 0}) {
			return
		}
		conn.Write([]byte{5, 0})
		head := make([]byte, 5)
		if _, err := io.ReadFull(conn, head); err != nil || head[3] != 3 {
			return
		}
		address := make([]byte, int(head[4])+2)
		if _, err := io.ReadFull(conn, address); err != nil {
			return
		}
		conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		request.Body.Close()
		seen <- string(address[:len(address)-2]) + request.URL.Path
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	}()
	return listener.Addr().(*net.TCPAddr).Port, seen
}

func TestIdentityProbesStayOnTheirOwnSOCKSListeners(t *testing.T) {
	for _, tc := range []struct{ ip, country string }{{"203.0.113.1", "DE"}, {"203.0.113.2", "NL"}} {
		tc := tc
		t.Run(tc.country, func(t *testing.T) {
			t.Parallel()
			port, seen := identitySOCKS(t, "ip="+tc.ip+"\nloc="+tc.country+"\n")
			status, _, body, err := probeHTTP(context.Background(), port, "http://profile-probe.invalid/trace", time.Second)
			if err != nil || status != 200 {
				t.Fatalf("probe: %d %v", status, err)
			}
			ip, country, err := parseIdentity(body)
			if err != nil || ip != tc.ip || country != tc.country {
				t.Fatalf("identity crossed profile: %q %q %v", ip, country, err)
			}
			select {
			case target := <-seen:
				if target != "profile-probe.invalid/trace" {
					t.Fatalf("target: %s", target)
				}
			case <-time.After(time.Second):
				t.Fatal("SOCKS target not observed")
			}
		})
	}
}

func TestProbeRejectsOversizedBody(t *testing.T) {
	port, _ := identitySOCKS(t, strings.Repeat("x", (64<<10)+1))
	_, _, _, err := probeHTTP(context.Background(), port, "http://profile-probe.invalid/", time.Second)
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("got %v", err)
	}
}

func TestProbeFailsWhenSOCKSIsUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	_, _, _, err = probeHTTP(context.Background(), port, "http://profile-probe.invalid/", time.Second)
	if err == nil {
		t.Fatal("probe succeeded without the isolated SOCKS listener")
	}
}

func TestResolveLiteralServerIP(t *testing.T) {
	ips, err := resolveServerIPs(context.Background(), "2001:db8::8")
	if err != nil || len(ips) != 1 || ips[0] != "2001:db8::8" {
		t.Fatalf("got %v %v", ips, err)
	}
}

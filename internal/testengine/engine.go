package testengine

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
	"github.com/OWNER/mikrotik-proxy-helper/internal/portpool"
	proc "github.com/OWNER/mikrotik-proxy-helper/internal/process"
	"github.com/OWNER/mikrotik-proxy-helper/internal/xrayconfig"
)

type Config struct {
	XrayBinary   string
	RuntimeDir   string
	TestURL      string
	StartTimeout time.Duration
	HTTPTimeout  time.Duration
	TotalTimeout time.Duration
	StopTimeout  time.Duration
	Concurrency  int
}

type Engine struct {
	cfg   Config
	ports *portpool.Pool
	slots chan struct{}
}

func New(cfg Config, ports *portpool.Pool) *Engine {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	return &Engine{cfg: cfg, ports: ports, slots: make(chan struct{}, cfg.Concurrency)}
}

func (e *Engine) TestProfile(parent context.Context, profile model.Profile) (result model.TestResult) {
	result = model.TestResult{RunID: newRunID(), ProfileID: profile.ID, ProfileName: profile.Name, Status: "start_failed", Stage: "validating", TestedAt: time.Now().UTC()}
	started := time.Now()
	defer func() { result.TotalMS = time.Since(started).Milliseconds() }()
	if !profile.Supported {
		result.Status, result.ErrorCode, result.ErrorMessage = "unsupported", "unsupported_profile", profile.Reason
		return
	}
	ctx, cancel := context.WithTimeout(parent, e.cfg.TotalTimeout)
	defer cancel()
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		return fail(result, "cancelled", "test_queue_cancelled", ctx.Err())
	}
	// Resolve for display only. Keep Xray's endpoint and DNS behavior unchanged.
	var errResolve error
	result.ServerIPs, errResolve = resolveServerIPs(ctx, profile.Host)
	if errResolve != nil {
		result.ServerIPError = "Server DNS lookup failed"
	}
	port, release, err := e.ports.Acquire()
	if err != nil {
		return fail(result, "start_failed", "port_unavailable", err)
	}
	defer release()
	cfg, err := xrayconfig.TestConfig(profile, port)
	if err != nil {
		return fail(result, "unsupported", "invalid_profile", err)
	}
	configPath, err := xrayconfig.WriteTemporary(e.cfg.RuntimeDir, result.RunID, cfg)
	if err != nil {
		return fail(result, "start_failed", "config_write_failed", err)
	}
	defer os.Remove(configPath)
	result.Stage = "starting"
	child, err := proc.Start(e.cfg.XrayBinary, configPath)
	if err != nil {
		return fail(result, "start_failed", "xray_start_failed", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), e.cfg.StopTimeout)
		defer stopCancel()
		_ = child.Stop(stopCtx)
	}()
	readyStarted := time.Now()
	if err := waitReady(ctx, child, port, e.cfg.StartTimeout); err != nil {
		if parent.Err() != nil {
			return fail(result, "cancelled", "test_cancelled", parent.Err())
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fail(result, "timeout", "start_timeout", err)
		}
		return fail(result, "start_failed", "socks_not_ready", err)
	}
	result.EndpointMS = time.Since(readyStarted).Milliseconds()
	result.Stage = "testing"
	status, ttfb, body, err := probeHTTP(ctx, port, e.cfg.TestURL, e.cfg.HTTPTimeout)
	result.HTTPStatus, result.TTFBMS = status, ttfb
	if err != nil {
		if parent.Err() != nil {
			return fail(result, "cancelled", "test_cancelled", parent.Err())
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fail(result, "timeout", "http_timeout", err)
		}
		return fail(result, "http_failed", "http_probe_failed", err)
	}
	// Identity must use this profile's isolated SOCKS listener, never the host
	// network or the active production tunnel. Reuse the default trace response.
	if e.cfg.TestURL != identityURL {
		_, _, body, err = probeHTTP(ctx, port, identityURL, e.cfg.HTTPTimeout)
	}
	if err != nil {
		result.IdentityError = "Exit identity request failed"
	} else {
		result.ExitIP, result.ExitCountry, err = parseIdentity(body)
		if err != nil {
			result.IdentityError = err.Error()
		}
	}
	if parent.Err() != nil {
		return fail(result, "cancelled", "test_cancelled", parent.Err())
	}
	result.Status, result.Stage = "healthy", "completed"
	return
}

func fail(r model.TestResult, status, code string, err error) model.TestResult {
	r.Status, r.Stage, r.ErrorCode = status, "completed", code
	if err != nil {
		r.ErrorMessage = err.Error()
	}
	return r
}

func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

func waitReady(ctx context.Context, child *proc.Child, port int, timeout time.Duration) error {
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	address := fmt.Sprintf("127.0.0.1:%d", port)
	for {
		if exited, err := child.Exited(); exited {
			return err
		}
		conn, err := (&net.Dialer{Timeout: 150 * time.Millisecond}).DialContext(readyCtx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-readyCtx.Done():
			return readyCtx.Err()
		case <-ticker.C:
		}
	}
}

func probeHTTP(parent context.Context, socksPort int, target string, timeout time.Duration) (int, int64, []byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", socksPort))
			if err != nil {
				return nil, err
			}
			if err := socksConnect(conn, address, timeout); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return conn, nil
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: timeout,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, 0, nil, err
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, nil, err
	}
	ttfb := time.Since(started).Milliseconds()
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if readErr != nil {
		return resp.StatusCode, ttfb, nil, readErr
	}
	if len(body) > 64<<10 {
		return resp.StatusCode, ttfb, nil, errors.New("HTTP response exceeds 64 KiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return resp.StatusCode, ttfb, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, ttfb, body, nil
}

func socksConnect(conn net.Conn, target string, timeout time.Duration) error {
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[0] != 5 || reply[1] != 0 {
		return errors.New("SOCKS authentication failed")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || len(host) > 255 {
		return errors.New("invalid SOCKS target")
	}
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, []byte(host)...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[1] != 0 {
		return fmt.Errorf("SOCKS connect error %d", head[1])
	}
	skip := 0
	switch head[3] {
	case 1:
		skip = 4
	case 4:
		skip = 16
	case 3:
		one := make([]byte, 1)
		if _, err := io.ReadFull(conn, one); err != nil {
			return err
		}
		skip = int(one[0])
	default:
		return errors.New("invalid SOCKS reply")
	}
	if _, err := io.CopyN(io.Discard, conn, int64(skip+2)); err != nil {
		return err
	}
	return conn.SetDeadline(time.Time{})
}

// Cloudflare reports the IP and country observed for this particular request.
const identityURL = "https://www.cloudflare.com/cdn-cgi/trace"

func parseIdentity(body []byte) (string, string, error) {
	values := make(map[string]string)
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && (key == "ip" || key == "loc") {
			if _, duplicate := values[key]; duplicate {
				return "", "", errors.New("Duplicate exit identity fields")
			}
			values[key] = value
		}
	}
	ip := net.ParseIP(values["ip"])
	if ip == nil {
		return "", "", errors.New("Exit identity response has no valid IP")
	}
	country := values["loc"]
	if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' || country == "XX" {
		return ip.String(), "", errors.New("Exit country is unavailable")
	}
	return ip.String(), country, nil
}

func resolveServerIPs(parent context.Context, host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var ips []string
	for _, address := range addresses {
		value := address.IP.String()
		if !seen[value] {
			ips = append(ips, value)
			seen[value] = true
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("No server IP addresses")
	}
	return ips, nil
}

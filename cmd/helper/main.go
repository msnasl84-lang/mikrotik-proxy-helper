package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
	"github.com/OWNER/mikrotik-proxy-helper/internal/events"
	"github.com/OWNER/mikrotik-proxy-helper/internal/portpool"
	"github.com/OWNER/mikrotik-proxy-helper/internal/results"
	"github.com/OWNER/mikrotik-proxy-helper/internal/testengine"
)

const version = "0.3.0-dev.10"

type Profile = model.Profile

type Health struct {
	Status        string    `json:"status"`
	Reason        string    `json:"reason,omitempty"`
	LatencyMS     int64     `json:"latency_ms,omitempty"`
	Failures      int       `json:"failures"`
	LastChecked   time.Time `json:"last_checked,omitempty"`
	LastSucceeded time.Time `json:"last_succeeded,omitempty"`
}

type State struct {
	Mode             string    `json:"mode"`
	AutomaticSwitch  bool      `json:"automatic_switching"`
	AutomaticFailback bool     `json:"automatic_failback"`
	SelectedProfile  string    `json:"selected_profile,omitempty"`
	ActiveProfile    string    `json:"active_profile,omitempty"`
	LastGoodProfile  string    `json:"last_good_profile,omitempty"`
	SubscriptionETag string    `json:"subscription_etag,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
	Health           Health    `json:"health"`
}

type Config struct {
	ListenAddr      string
	SubscriptionURL string
	DataDir         string
	XrayConfigDir   string
	SocksAddr       string
	HealthURL       string
	HealthInterval  time.Duration
	HealthTimeout   time.Duration
	FailureLimit    int
	AuthUser        string
	AuthPassword    string
	XrayBinary       string
	RuntimeDir       string
	TestURL          string
	TestStartTimeout time.Duration
	TestHTTPTimeout  time.Duration
	TestTotalTimeout time.Duration
}

type App struct {
	mu       sync.RWMutex
	ctx      context.Context
	cfg      Config
	state    State
	profiles []Profile
	client   *http.Client
	tests    *testengine.Engine
	lastTests map[string]model.TestResult
	subscriptionError string
	events   *events.Broker
	results  *results.Store
	runMu    sync.Mutex
	activeRun string
	activeCancel context.CancelFunc
}

type ActionResponse struct {
	OK        bool      `json:"ok"`
	Action    string    `json:"action"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Details   any       `json:"details,omitempty"`
}

type RouterStatus struct {
	Mode          string    `json:"mode"`
	DesiredMode   string    `json:"desired_mode,omitempty"`
	ActiveProfile string    `json:"active_profile,omitempty"`
	Xray          string    `json:"xray,omitempty"`
	Tun2Socks     string    `json:"tun2socks,omitempty"`
	Route         string    `json:"route,omitempty"`
	Message       string    `json:"message,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(env(key, ""))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func main() {
	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := Config{
		ListenAddr:      env("LISTEN_ADDR", ":8080"),
		SubscriptionURL: env("SUBSCRIPTION_URL", ""),
		DataDir:         env("DATA_DIR", "/data"),
		XrayConfigDir:   env("XRAY_CONFIG_DIR", "/shared/xray-config"),
		SocksAddr:       env("SOCKS_ADDR", "172.19.0.2:1080"),
		HealthURL:       env("HEALTH_URL", "https://www.cloudflare.com/cdn-cgi/trace"),
		HealthInterval:  time.Duration(envInt("HEALTH_INTERVAL_SECONDS", 60)) * time.Second,
		HealthTimeout:   time.Duration(envInt("HEALTH_TIMEOUT_SECONDS", 10)) * time.Second,
		FailureLimit:    envInt("HEALTH_FAILURE_LIMIT", 3),
		AuthUser:        env("HELPER_USER", "admin"),
		AuthPassword:    env("HELPER_PASSWORD", ""),
		XrayBinary:      env("XRAY_BINARY", "/usr/local/bin/xray"),
		RuntimeDir:      filepath.Join(env("DATA_DIR", "/data"), "runtime"),
		TestURL:         env("TEST_URL", "https://www.cloudflare.com/cdn-cgi/trace"),
		TestStartTimeout: time.Duration(envInt("TEST_START_TIMEOUT_SECONDS", 4)) * time.Second,
		TestHTTPTimeout: time.Duration(envInt("TEST_HTTP_TIMEOUT_SECONDS", 10)) * time.Second,
		TestTotalTimeout: time.Duration(envInt("TEST_TOTAL_TIMEOUT_SECONDS", 15)) * time.Second,
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	if err := cleanupRuntime(cfg.RuntimeDir); err != nil { return err }
	lockFile, err := acquireInstanceLock(filepath.Join(cfg.DataDir, "helper.lock"))
	if err != nil { return err }
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); _ = lockFile.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ports, err := portpool.New(envInt("TEST_PORT_MIN", 12000), envInt("TEST_PORT_MAX", 12031))
	if err != nil { return err }
	app := &App{ctx: ctx, cfg: cfg, state: State{Mode: "manual-health", Health: Health{Status: "unknown"}}, lastTests: make(map[string]model.TestResult), events: events.New()}
	app.results = results.New(filepath.Join(cfg.DataDir, "test-results.json"), 20)
	if err := app.results.Load(); err != nil { log.Printf("test results load: %v", err) }
	for id, result := range restoreLastTests(app.results.Snapshot()) { app.lastTests[id] = result }
	app.tests = testengine.New(testengine.Config{XrayBinary:cfg.XrayBinary, RuntimeDir:cfg.RuntimeDir, TestURL:cfg.TestURL, StartTimeout:cfg.TestStartTimeout, HTTPTimeout:cfg.TestHTTPTimeout, TotalTimeout:cfg.TestTotalTimeout, StopTimeout:2*time.Second, Concurrency:envInt("TEST_ALL_CONCURRENCY", 1)}, ports)
	app.client = app.proxyHTTPClient()
	if err := app.loadState(); err != nil {
		log.Printf("state load: %v", err)
	}
	if cfg.SubscriptionURL != "" {
		if err := app.refreshSubscription(ctx); err != nil {
			log.Printf("initial subscription refresh: %v", err)
			app.subscriptionError = err.Error()
		}
	}
	go app.healthLoop(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.auth(app.handleIndex))
	mux.HandleFunc("/api/status", app.auth(app.handleStatus))
	mux.HandleFunc("/api/refresh", app.auth(app.handleRefresh))
	mux.HandleFunc("/api/select", app.auth(app.handleSelect))
	mux.HandleFunc("/api/activate", app.auth(app.handleActivate))
	mux.HandleFunc("/api/mode", app.auth(app.handleMode))
	mux.HandleFunc("/api/health", app.auth(app.handleHealth))
	mux.HandleFunc("/api/tests/profile", app.auth(app.handleTestProfile))
	mux.HandleFunc("/api/tests/all", app.auth(app.handleTestAll))
	mux.HandleFunc("/api/tests/cancel", app.auth(app.handleCancelTest))
	mux.HandleFunc("/api/tests", app.auth(app.handleTestHistory))
	mux.HandleFunc("/api/events", app.auth(app.handleEvents))
	server := &http.Server{Addr: cfg.ListenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("mikrotik-proxy-helper %s listening on %s", version, cfg.ListenAddr)
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) { return fmt.Errorf("HTTP server: %w", err) }
		return nil
	case <-ctx.Done():
		log.Printf("shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil { return fmt.Errorf("HTTP shutdown: %w", err) }
		log.Printf("shutdown complete")
		return nil
	}
}

func cleanupRuntime(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil { return err }
	entries, err := os.ReadDir(dir)
	if err != nil { return err }
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "probe-") && strings.HasSuffix(entry.Name(), ".json") {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil { return err }
		}
	}
	return nil
}

func acquireInstanceLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil { return nil, fmt.Errorf("open instance lock: %w", err) }
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another helper instance is already running")
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	}
	return f, nil
}

func (a *App) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.AuthPassword != "" {
			user, password, ok := r.BasicAuth()
			userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.cfg.AuthUser)) == 1
			passwordOK := subtle.ConstantTimeCompare([]byte(password), []byte(a.cfg.AuthPassword)) == 1
			if !ok || !userOK || !passwordOK {
				w.Header().Set("WWW-Authenticate", `Basic realm="MikroTik Proxy Helper"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (a *App) loadState() error {
	data, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &a.state)
}

func (a *App) saveStateLocked() error {
	a.state.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(filepath.Join(a.cfg.DataDir, "state.json"), a.state, 0o600)
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func restoreLastTests(runs []results.Run) map[string]model.TestResult {
	last := make(map[string]model.TestResult)
	for _, run := range runs {
		for _, result := range run.Results {
			if result.ProfileID != "" { last[result.ProfileID] = result }
		}
	}
	return last
}

func (a *App) refreshSubscription(ctx context.Context) error {
	if a.cfg.SubscriptionURL == "" {
		return errors.New("SUBSCRIPTION_URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.SubscriptionURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	profiles, err := parseSubscription(body)
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		return errors.New("subscription contains no profiles")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.profiles = profiles
	a.subscriptionError = ""
	a.state.SubscriptionETag = resp.Header.Get("ETag")
	if err := writeJSONAtomic(filepath.Join(a.cfg.DataDir, "profiles.json"), profiles, 0o600); err != nil {
		return err
	}
	return a.saveStateLocked()
}

func parseSubscription(body []byte) ([]Profile, error) {
	text := strings.TrimSpace(string(body))
	decoded, err := base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' { return -1 }
		return r
	}, text))
	if err == nil && strings.Contains(string(decoded), "://") {
		text = string(decoded)
	}
	lines := strings.FieldsFunc(text, func(r rune) bool { return r == '\r' || r == '\n' })
	seen := map[string]bool{}
	profiles := make([]Profile, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" { continue }
		p := parseProfile(line)
		if p.ID == "" || seen[p.ID] { continue }
		seen[p.ID] = true
		profiles = append(profiles, p)
	}
	sort.SliceStable(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

func parseProfile(raw string) Profile {
	p := Profile{Raw: raw, RequiredCore: "unsupported", Supported: false}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		p.Reason = "invalid URI"
		return p
	}
	p.Scheme = strings.ToLower(u.Scheme)
	p.Host = u.Hostname()
	p.Port = 0
	if value, err := strconv.Atoi(u.Port()); err == nil { p.Port = value }
	p.Name, _ = url.QueryUnescape(strings.TrimPrefix(u.Fragment, "#"))
	if p.Name == "" { p.Name = fmt.Sprintf("%s:%d", p.Host, p.Port) }
	q := u.Query()
	p.Type = strings.ToLower(q.Get("type"))
	p.Security = strings.ToLower(q.Get("security"))
	p.HeaderType = strings.ToLower(q.Get("headerType"))
	p.Path = q.Get("path")
	p.SNI = q.Get("sni")
	if p.SNI == "" { p.SNI = q.Get("serverName") }
	if p.Scheme == "vless" {
		p.UUID = u.User.Username()
		p.RequiredCore = "xray"
		p.Supported = p.Host != "" && p.Port > 0 && validUUID(p.UUID) &&
			(p.Type == "" || p.Type == "tcp" || p.Type == "raw") &&
			(p.Security == "" || p.Security == "none")
		if !p.Supported { p.Reason = "v0.3 supports VLESS raw/TCP with security=none only" }
	} else {
		p.Reason = "protocol detected but not enabled in v0.3"
	}
	identity := strings.Join([]string{p.Scheme, p.UUID, strings.ToLower(p.Host), strconv.Itoa(p.Port), q.Encode()}, "|")
	sum := sha256.Sum256([]byte(identity))
	p.ID = hex.EncodeToString(sum[:8])
	return p
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' { return false }
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 { continue }
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) { return false }
	}
	return true
}

func (a *App) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "POST required", http.StatusMethodNotAllowed); return }
	if err := a.refreshSubscription(r.Context()); err != nil { a.writeAction(w, http.StatusBadGateway, false, "refresh", "Subscription refresh failed", map[string]any{"error":err.Error()}); return }
	a.mu.RLock(); count := len(a.profiles); a.mu.RUnlock()
	a.writeAction(w, http.StatusOK, true, "refresh", "Subscription refreshed successfully", map[string]any{"profiles":count})
}

func (a *App) handleSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "POST required", http.StatusMethodNotAllowed); return }
	id := strings.TrimSpace(r.FormValue("id"))
	a.mu.Lock()
	defer a.mu.Unlock()
	var selected *Profile
	for i := range a.profiles {
		if a.profiles[i].ID == id { selected = &a.profiles[i]; break }
	}
	if selected == nil { a.writeAction(w,http.StatusNotFound,false,"prepare","Profile not found",nil); return }
	if !selected.Supported { a.writeAction(w,http.StatusUnprocessableEntity,false,"prepare","Profile is not supported",map[string]any{"reason":selected.Reason}); return }
	config := makeXrayConfig(*selected)
	if err := writeJSONAtomic(filepath.Join(a.cfg.XrayConfigDir, "config.pending.json"), config, 0o600); err != nil {
		a.writeAction(w,http.StatusInternalServerError,false,"prepare","Could not write pending configuration",map[string]any{"error":err.Error()}); return
	}
	a.state.SelectedProfile = selected.ID
	_ = a.saveStateLocked()
	a.writeAction(w,http.StatusOK,true,"prepare","Pending configuration created",map[string]any{"profile_id":selected.ID,"name":selected.Name,"endpoint":net.JoinHostPort(selected.Host,strconv.Itoa(selected.Port)),"protocol":selected.Scheme+"/"+selected.Type})
}

func (a *App) handleActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "POST required", http.StatusMethodNotAllowed); return }
	id := strings.TrimSpace(r.FormValue("id"))
	a.mu.RLock()
	selected := a.state.SelectedProfile
	var name string
	for i := range a.profiles { if a.profiles[i].ID == id { name = a.profiles[i].Name; break } }
	a.mu.RUnlock()
	if id == "" || id != selected { a.writeAction(w,http.StatusConflict,false,"activate","Prepare this profile before activation",nil); return }
	if name == "" { a.writeAction(w,http.StatusNotFound,false,"activate","Profile not found",nil); return }
	request := map[string]any{"action":"apply-profile","profile_id":id,"created_at":time.Now().UTC()}
	if err := writeJSONAtomic(filepath.Join(a.cfg.DataDir, "apply-request.json"), request, 0o600); err != nil {
		a.writeAction(w,http.StatusInternalServerError,false,"activate","Could not write activation request",map[string]any{"error":err.Error()}); return
	}
	a.writeAction(w,http.StatusAccepted,true,"activate","Activation request queued",map[string]any{"profile_id":id,"name":name})
}

func (a *App) handleMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "POST required", http.StatusMethodNotAllowed); return }
	mode := strings.ToLower(strings.TrimSpace(r.FormValue("mode")))
	allowed := map[string]bool{"vless":true,"blocked":true,"direct":true}
	if !allowed[mode] { a.writeAction(w,http.StatusUnprocessableEntity,false,"mode","Unsupported mode",nil); return }
	request := map[string]any{"action":"set-mode","mode":mode,"created_at":time.Now().UTC()}
	if err := writeJSONAtomic(filepath.Join(a.cfg.DataDir, "mode-request.json"), request, 0o600); err != nil {
		a.writeAction(w,http.StatusInternalServerError,false,"mode","Could not write mode request",map[string]any{"error":err.Error()}); return
	}
	a.writeAction(w,http.StatusAccepted,true,"mode","Mode change request queued",map[string]any{"mode":mode})
}

func (a *App) routerStatus() RouterStatus {
	var status RouterStatus
	b, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "router-status.json"))
	if err == nil { _ = json.Unmarshal(b, &status) }
	return status
}

func makeXrayConfig(p Profile) map[string]any {
	headerType := p.HeaderType
	if headerType == "" { headerType = "none" }
	rawSettings := map[string]any{"header": map[string]any{"type": headerType}}
	if headerType == "http" {
		request := map[string]any{}
		if p.Path != "" { request["path"] = []string{p.Path} }
		rawSettings["header"] = map[string]any{"type":"http", "request":request}
	}
	stream := map[string]any{"method":"raw", "security":p.Security, "rawSettings":rawSettings}
	if stream["security"] == "" { stream["security"] = "none" }
	return map[string]any{
		"log": map[string]any{"loglevel":"warning"},
		"inbounds": []any{map[string]any{"tag":"socks-in","listen":"0.0.0.0","port":1080,"protocol":"socks","settings":map[string]any{"auth":"noauth","udp":true}}},
		"outbounds": []any{map[string]any{"tag":"selected-proxy","protocol":"vless","settings":map[string]any{"vnext":[]any{map[string]any{"address":p.Host,"port":p.Port,"users":[]any{map[string]any{"id":p.UUID,"encryption":"none"}}}}},"streamSettings":stream}},
		"routing": map[string]any{"domainStrategy":"AsIs","rules":[]any{map[string]any{"type":"field","inboundTag":[]string{"socks-in"},"outboundTag":"selected-proxy"}}},
	}
}

func (a *App) proxyHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: a.cfg.HealthTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, "tcp", a.cfg.SocksAddr)
			if err != nil { return nil, err }
			if err := socksConnect(conn, address); err != nil { conn.Close(); return nil, err }
			return conn, nil
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: a.cfg.HealthTimeout,
	}
	return &http.Client{Transport: transport, Timeout: a.cfg.HealthTimeout}
}

func socksConnect(conn net.Conn, target string) error {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte{5,1,0}); err != nil { return err }
	reply := make([]byte,2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[0] != 5 || reply[1] != 0 { return errors.New("SOCKS authentication failed") }
	host, portText, err := net.SplitHostPort(target)
	if err != nil { return err }
	port, err := strconv.Atoi(portText); if err != nil { return err }
	request := []byte{5,1,0,3,byte(len(host))}
	request = append(request, []byte(host)...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil { return err }
	head := make([]byte,4)
	if _, err := io.ReadFull(conn, head); err != nil { return err }
	if head[1] != 0 { return fmt.Errorf("SOCKS connect error %d", head[1]) }
	var skip int
	switch head[3] { case 1: skip=4; case 4: skip=16; case 3: one:=make([]byte,1); if _,err:=io.ReadFull(conn,one);err!=nil{return err}; skip=int(one[0]); default:return errors.New("invalid SOCKS reply") }
	if _, err := io.CopyN(io.Discard, conn, int64(skip+2)); err != nil { return err }
	_ = conn.SetDeadline(time.Time{})
	return nil
}

func (a *App) runHealth(ctx context.Context) Health {
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.HealthURL, nil)
	if err == nil { req.Header.Set("User-Agent", "mikrotik-proxy-helper/"+version) }
	if err == nil {
		resp, doErr := a.client.Do(req); err = doErr
		if resp != nil { io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)); resp.Body.Close(); if resp.StatusCode < 200 || resp.StatusCode >= 400 { err = fmt.Errorf("HTTP %d", resp.StatusCode) } }
	}
	a.mu.Lock(); defer a.mu.Unlock()
	h := a.state.Health
	h.LastChecked = time.Now().UTC()
	if err == nil {
		h.Status="healthy"; h.Reason=""; h.Failures=0; h.LatencyMS=time.Since(started).Milliseconds(); h.LastSucceeded=h.LastChecked
		if a.state.ActiveProfile != "" { a.state.LastGoodProfile = a.state.ActiveProfile }
	} else {
		h.Failures++; h.Reason=err.Error(); h.LatencyMS=0
		if h.Failures >= a.cfg.FailureLimit { h.Status="unhealthy" } else { h.Status="degraded" }
	}
	a.state.Health=h; _=a.saveStateLocked(); return h
}

func (a *App) healthLoop(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.HealthInterval); defer ticker.Stop()
	modeTicker := time.NewTicker(time.Second); defer modeTicker.Stop()
	lastMode := ""
	for {
		select {
		case <-ctx.Done(): return
		case <-modeTicker.C:
			mode := a.routerStatus().Mode
			if mode != lastMode {
				lastMode = mode
				if mode == "vless" {
					a.mu.Lock()
					a.state.Health = Health{Status:"checking"}
					a.mu.Unlock()
					checkCtx,cancel:=context.WithTimeout(ctx,a.cfg.HealthTimeout); a.runHealth(checkCtx); cancel()
				}
			}
		case <-ticker.C:
			if a.routerStatus().Mode != "vless" { continue }
			checkCtx,cancel:=context.WithTimeout(ctx,a.cfg.HealthTimeout); a.runHealth(checkCtx); cancel()
		}
	}
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w,"POST required",http.StatusMethodNotAllowed); return }
	ctx,cancel:=context.WithTimeout(r.Context(),a.cfg.HealthTimeout); defer cancel(); h:=a.runHealth(ctx)
	ok := h.Status == "healthy"
	status := http.StatusOK; message := "Active tunnel is healthy"
	if !ok { status = http.StatusBadGateway; message = "Active tunnel health check failed" }
	a.writeAction(w,status,ok,"health",message,h)
}

func (a *App) handleTestProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w,"POST required",http.StatusMethodNotAllowed); return }
	id := strings.TrimSpace(r.FormValue("id"))
	a.mu.RLock()
	var profile *Profile
	for i := range a.profiles { if a.profiles[i].ID == id { copy := a.profiles[i]; profile = &copy; break } }
	a.mu.RUnlock()
	if profile == nil { a.writeAction(w,http.StatusNotFound,false,"test-profile","Profile not found",nil); return }
	testCtx, cancel := context.WithCancel(r.Context())
	stopOnShutdown := context.AfterFunc(a.ctx, cancel)
	defer func() { stopOnShutdown(); cancel() }()
	result := a.tests.TestProfile(testCtx, *profile)
	a.mu.Lock(); a.lastTests[result.ProfileID] = result; a.mu.Unlock()
	run := results.Run{ID:result.RunID, Status:"completed", Results:[]model.TestResult{result}, StartedAt:result.TestedAt.Format(time.RFC3339Nano), EndedAt:time.Now().UTC().Format(time.RFC3339Nano)}
	if err := a.results.Save(run); err != nil { log.Printf("save profile test result: %v", err) }
	status := http.StatusOK
	message := "Profile test completed"
	if result.Status != "healthy" { status = http.StatusBadGateway; message = "Profile test failed" }
	a.writeAction(w,status,result.Status == "healthy","test-profile",message,result)
}

func (a *App) handleTestAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w,"POST required",http.StatusMethodNotAllowed); return }
	a.runMu.Lock()
	if a.activeRun != "" {
		runID := a.activeRun
		a.runMu.Unlock()
		a.writeAction(w,http.StatusConflict,false,"test-all","Another Test All run is active",map[string]any{"run_id":runID})
		return
	}
	runID := fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
	runCtx, cancel := context.WithCancel(a.ctx)
	a.activeRun, a.activeCancel = runID, cancel
	a.runMu.Unlock()
	a.mu.RLock(); profiles := append([]Profile(nil), a.profiles...); a.mu.RUnlock()
	startedAt := time.Now().UTC()
	a.events.Publish(events.Event{Type:"run-started", Data:map[string]any{"run_id":runID,"total":len(profiles),"started_at":startedAt}})
	go func() {
		defer cancel()
		collected := a.tests.TestAll(runCtx, profiles, func(index, total int, result model.TestResult) {
			a.mu.Lock(); a.lastTests[result.ProfileID] = result; a.mu.Unlock()
			a.events.Publish(events.Event{Type:"profile-result", Data:map[string]any{"run_id":runID,"index":index,"total":total,"result":result}})
		})
		status := "completed"
		if runCtx.Err() != nil { status = "cancelled" }
		run := results.Run{ID:runID, Status:status, Results:collected, StartedAt:startedAt.Format(time.RFC3339Nano), EndedAt:time.Now().UTC().Format(time.RFC3339Nano)}
		if err := a.results.Save(run); err != nil { log.Printf("save test results: %v", err) }
		a.events.Publish(events.Event{Type:"run-completed", Data:run})
		a.runMu.Lock()
		if a.activeRun == runID { a.activeRun, a.activeCancel = "", nil }
		a.runMu.Unlock()
	}()
	w.Header().Set("Retry-After", "1")
	a.writeAction(w,http.StatusAccepted,true,"test-all","Test All started",map[string]any{"run_id":runID,"profiles":len(profiles)})
}

func (a *App) handleCancelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w,"POST required",http.StatusMethodNotAllowed); return }
	a.runMu.Lock(); runID, cancel := a.activeRun, a.activeCancel; a.runMu.Unlock()
	if cancel == nil { a.writeAction(w,http.StatusConflict,false,"cancel-test","No Test All run is active",nil); return }
	cancel()
	a.writeAction(w,http.StatusOK,true,"cancel-test","Cancellation requested",map[string]any{"run_id":runID})
}

func (a *App) handleTestHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { http.Error(w,"GET required",http.StatusMethodNotAllowed); return }
	w.Header().Set("Content-Type","application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"runs":a.results.Snapshot()})
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { http.Error(w,"GET required",http.StatusMethodNotAllowed); return }
	flusher, ok := w.(http.Flusher)
	if !ok { http.Error(w,"streaming unsupported",http.StatusInternalServerError); return }
	w.Header().Set("Content-Type","text/event-stream")
	w.Header().Set("Cache-Control","no-cache")
	w.Header().Set("Connection","keep-alive")
	stream, unsubscribe := a.events.Subscribe(); defer unsubscribe()
	keepalive := time.NewTicker(15*time.Second); defer keepalive.Stop()
	_, _ = io.WriteString(w, ": connected\n\n"); flusher.Flush()
	for {
		select {
		case <-a.ctx.Done(): return
		case <-r.Context().Done(): return
		case payload, open := <-stream:
			if !open { return }
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload); flusher.Flush()
		case <-keepalive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n"); flusher.Flush()
		}
	}
}

func (a *App) writeAction(w http.ResponseWriter, status int, ok bool, action, message string, details any) {
	w.Header().Set("Content-Type","application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ActionResponse{OK:ok,Action:action,Message:message,Timestamp:time.Now().UTC(),Details:details})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) { a.writeStatus(w) }
func (a *App) writeStatus(w http.ResponseWriter) { a.mu.RLock(); defer a.mu.RUnlock(); a.writeStatusLocked(w) }
func (a *App) writeStatusLocked(w http.ResponseWriter) {
	w.Header().Set("Content-Type","application/json")
	a.runMu.Lock(); activeRun := a.activeRun; a.runMu.Unlock()
	router := a.routerStatus()
	state := a.state
	if router.Mode != "vless" {
		state.Health = Health{Status:"inactive"}
	}
	profiles := a.profiles
	if profiles == nil { profiles = []Profile{} }
	json.NewEncoder(w).Encode(map[string]any{"version":version,"state":state,"router":router,"profiles":profiles,"subscription_error":a.subscriptionError,"last_tests":a.lastTests,"active_run":activeRun})
}

const page = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>MikroTik Proxy Helper</title><style>
:root{color-scheme:dark;--bg:#0b1016;--panel:#121a23;--line:#293443;--text:#edf3f8;--muted:#98a8b8;--cyan:#67e8f9;--green:#4ade80;--yellow:#facc15;--red:#fb7185}*{box-sizing:border-box}body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;max-width:1120px;margin:0 auto;padding:28px 18px 50px;background:var(--bg);color:var(--text)}h1{margin:0 0 18px}.cards{display:grid;grid-template-columns:repeat(3,1fr);gap:12px;margin-bottom:18px}.card,.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:16px}.label{font-size:.78rem;color:var(--muted);text-transform:uppercase}.value{margin-top:6px;font-weight:700;color:var(--cyan)}.toolbar{display:flex;gap:10px;flex-wrap:wrap;margin:18px 0}button{border:1px solid #42536a;border-radius:8px;background:#1c2a3a;color:var(--text);padding:9px 14px;cursor:pointer;font-weight:650}button:hover{background:#26384d}button:disabled{opacity:.5;cursor:not-allowed}button.busy{cursor:progress}table{width:100%;border-collapse:collapse}th,td{padding:11px 9px;border-bottom:1px solid var(--line);text-align:left}th{color:var(--muted);font-size:.85rem}.selected{background:#102a25}.ok{color:var(--green)}.warn{color:var(--yellow)}.bad{color:var(--red)}#log{height:235px;overflow:auto;white-space:pre-wrap;margin:0;background:#080c11;border:1px solid var(--line);border-radius:8px;padding:12px;color:#cbd5e1;font:13px/1.55 ui-monospace,SFMono-Regular,Consolas,monospace}.panel-head{display:flex;align-items:center;justify-content:space-between;margin-bottom:12px}.panel-head h2{margin:0;font-size:1.15rem}.small{font-size:.84rem;color:var(--muted)}@media(max-width:720px){.cards{grid-template-columns:1fr}table{display:block;overflow-x:auto}}
</style></head><body><h1>MikroTik Proxy Helper</h1><section class="cards"><div class="card"><div class="label">Router mode</div><div class="value" id="mode">Loading…</div></div><div class="card"><div class="label">Health</div><div class="value" id="health">Loading…</div></div><div class="card"><div class="label">Latency</div><div class="value" id="latency">—</div></div></section><div class="toolbar"><button data-mode="vless">Enable VLESS</button><button data-mode="blocked">Disable VLESS</button><button data-mode="direct">Direct WAN</button></div><div class="toolbar"><button id="refresh">Refresh subscription</button><button id="test">Test active tunnel</button><button id="testAll">Test all tunnels</button><button id="cancelTest">Cancel current test</button></div><section class="panel"><div class="panel-head"><h2>Service state</h2><span class="small" id="services">Waiting for RouterOS status…</span></div></section><section class="panel" style="margin-top:18px"><div class="panel-head"><h2>Profiles</h2><span class="small" id="count"></span></div><p class="small">Status and latency show the last isolated profile test, not the active tunnel. Results may predate a WAN change.</p><p class="small bad" id="subscriptionError"></p><table><thead><tr><th>Name</th><th>Endpoint</th><th>Protocol</th><th>Supported</th><th>Last isolated test</th><th>Latency (TTFB)</th><th>Action</th></tr></thead><tbody id="profiles"></tbody></table></section><section class="panel" style="margin-top:18px"><div class="panel-head"><h2>Operation log</h2><button id="clear">Clear log</button></div><pre id="log" aria-live="polite"></pre></section><script>
const logBox=document.getElementById('log');
function line(message,kind='info'){const t=new Date().toLocaleTimeString();const mark=kind==='ok'?'OK':kind==='bad'?'ERROR':'INFO';logBox.textContent+='['+t+'] ['+mark+'] '+message+'\n';logBox.scrollTop=logBox.scrollHeight}
function text(id,value){document.getElementById(id).textContent=value}
function healthClass(status){return status==='healthy'?'ok':status==='unhealthy'?'bad':'warn'}
function failureDetails(details){if(!details)return '';const code=details.error_code||'';const message=details.error_message||details.error||'';if(code&&message)return code+': '+message;return message||code}
async function request(path,options={}){const response=await fetch(path,options);let data;try{data=await response.json()}catch{throw new Error('HTTP '+response.status)}if(!response.ok||data.ok===false){const detail=failureDetails(data.details);throw new Error(data.message+(detail?': '+detail:''))}return data}
async function loadStatus(){const data=await request('/api/status');const state=data.state;const router=data.router||{};text('mode',router.mode||'unknown');text('services','Xray: '+(router.xray||'unknown')+' · tun2socks: '+(router.tun2socks||'unknown')+' · route: '+(router.route||'unknown'));const h=document.getElementById('health');h.textContent=state.health.status;h.className='value '+healthClass(state.health.status);text('latency',state.health.latency_ms?state.health.latency_ms+' ms':'—');text('subscriptionError',data.subscription_error?'Subscription refresh failed: '+data.subscription_error:'');renderProfiles(Array.isArray(data.profiles)?data.profiles:[],state.selected_profile,router.active_profile||state.active_profile,data.last_tests||{});return data}
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
let statusRefreshRunning=false;
async function refreshStatusSilently(){if(statusRefreshRunning)return;statusRefreshRunning=true;try{await loadStatus()}catch{}finally{statusRefreshRunning=false}}
async function waitForMode(mode,timeoutMS=45000){const deadline=Date.now()+timeoutMS;let lastError;while(Date.now()<deadline){try{const data=await loadStatus();if((data.router||{}).mode===mode)return data}catch(e){lastError=e}await sleep(1500)}throw lastError||new Error('Timed out waiting for RouterOS mode '+mode)}
function renderProfiles(profiles,selected,active,lastTests){const body=document.getElementById('profiles');body.replaceChildren();text('count',profiles.length+' profile(s)');if(!profiles.length){const row=document.createElement('tr');const cell=document.createElement('td');cell.colSpan=7;cell.textContent='No profiles available. Check subscription and refresh it.';row.appendChild(cell);body.appendChild(row)}const healthy=Object.values(lastTests).filter(r=>r.status==='healthy'&&r.ttfb_ms>0);const bestLatency=healthy.length?Math.min(...healthy.map(r=>r.ttfb_ms)):0;for(const p of profiles){const tr=document.createElement('tr');if(p.id===active)tr.className='selected';for(const value of [p.name,p.host+':'+p.port,p.scheme+'/'+(p.type||'raw')]){const td=document.createElement('td');td.textContent=value;tr.appendChild(td)}const supported=document.createElement('td');supported.textContent=p.supported?'Yes':'No — '+(p.reason||'unsupported');supported.className=p.supported?'ok':'bad';tr.appendChild(supported);const result=lastTests[p.id];const last=document.createElement('td');last.textContent=result?result.status+' · '+(result.tested_at?new Date(result.tested_at).toLocaleString():'time unknown'):'—';last.className=result?(result.status==='healthy'?'ok':result.status==='unsupported'?'warn':'bad'):'';tr.appendChild(last);const latency=document.createElement('td');if(result&&result.status==='healthy'&&result.ttfb_ms>0){const isBest=result.ttfb_ms===bestLatency;latency.textContent=result.ttfb_ms+' ms'+(isBest?' · Best':'');latency.className=isBest?'ok':''}else{latency.textContent='—'}tr.appendChild(latency);const action=document.createElement('td');const prepareButton=document.createElement('button');prepareButton.textContent=p.id===selected?'Prepared':'Prepare';prepareButton.disabled=!p.supported;prepareButton.onclick=()=>prepare(p,prepareButton);action.appendChild(prepareButton);const activateButton=document.createElement('button');activateButton.textContent=p.id===active?'Active':'Activate';activateButton.style.marginLeft='6px';activateButton.disabled=!p.supported||p.id!==selected||p.id===active;activateButton.onclick=()=>activate(p,activateButton);action.appendChild(activateButton);const testButton=document.createElement('button');testButton.textContent='Test';testButton.style.marginLeft='6px';testButton.disabled=!p.supported;testButton.onclick=()=>testProfile(p,testButton);action.appendChild(testButton);tr.appendChild(action);body.appendChild(tr)}}
async function busy(button,work){button.disabled=true;button.classList.add('busy');const old=button.textContent;button.textContent='Working…';try{await work()}finally{button.disabled=false;button.classList.remove('busy');button.textContent=old}}
async function prepare(profile,button){line('Preparing profile: '+profile.name);await busy(button,async()=>{try{const form=new URLSearchParams({id:profile.id});const r=await request('/api/select',{method:'POST',body:form});line(r.message+' — '+r.details.name+' — '+r.details.endpoint,'ok');await loadStatus()}catch(e){line(e.message,'bad')}})}
async function activate(profile,button){line('Requesting activation: '+profile.name);await busy(button,async()=>{try{const form=new URLSearchParams({id:profile.id});const r=await request('/api/activate',{method:'POST',body:form});line(r.message+' — waiting for RouterOS','ok')}catch(e){line(e.message,'bad')}finally{setTimeout(loadStatus,2500)}})}
async function testProfile(profile,button){line('Testing profile in an isolated Xray: '+profile.name);await busy(button,async()=>{try{const form=new URLSearchParams({id:profile.id});const r=await request('/api/tests/profile',{method:'POST',body:form});line(r.message+' — '+r.details.status+' — TTFB '+r.details.ttfb_ms+' ms','ok')}catch(e){line(e.message,'bad')}finally{await loadStatus()}})}
document.getElementById('refresh').onclick=e=>busy(e.currentTarget,async()=>{line('Downloading subscription…');try{const r=await request('/api/refresh',{method:'POST'});line(r.message+' — profiles found: '+r.details.profiles,'ok');await loadStatus()}catch(e){line(e.message,'bad')}});
document.getElementById('test').onclick=e=>busy(e.currentTarget,async()=>{line('Testing active tunnel…');try{const r=await request('/api/health',{method:'POST'});line(r.message+' — latency: '+r.details.latency_ms+' ms','ok');await loadStatus()}catch(e){line(e.message,'bad');await loadStatus()}});
document.getElementById('testAll').onclick=e=>busy(e.currentTarget,async()=>{line('Starting sequential Test All…');try{const r=await request('/api/tests/all',{method:'POST'});line(r.message+' — '+r.details.profiles+' profiles','ok')}catch(e){line(e.message,'bad')}});
document.getElementById('cancelTest').onclick=e=>busy(e.currentTarget,async()=>{try{const r=await request('/api/tests/cancel',{method:'POST'});line(r.message+' — '+r.details.run_id)}catch(e){line(e.message,'bad')}});
document.getElementById('clear').onclick=()=>{logBox.textContent=''};
for(const button of document.querySelectorAll('[data-mode]'))button.onclick=e=>busy(e.currentTarget,async()=>{const mode=e.currentTarget.dataset.mode;if(mode==='direct'&&!confirm('Direct mode bypasses the VPN kill switch. Continue?'))return;try{const form=new URLSearchParams({mode});const r=await request('/api/mode',{method:'POST',body:form});line(r.message+' — '+mode+' — waiting for RouterOS','ok');await waitForMode(mode);line('Router mode applied — '+mode,'ok')}catch(err){line(err.message,'bad');await refreshStatusSilently()}});
const eventStream=new EventSource('/api/events');eventStream.onmessage=async event=>{try{const message=JSON.parse(event.data);if(message.type==='profile-result'){const r=message.data.result;const detail=r.status==='healthy'?' — TTFB '+r.ttfb_ms+' ms':(failureDetails(r)?' — '+failureDetails(r):'');line('['+(message.data.index+1)+'/'+message.data.total+'] '+r.profile_name+' — '+r.status+detail,r.status==='healthy'?'ok':r.status==='unsupported'?'info':'bad');await loadStatus()}else if(message.type==='run-completed'){line('Test All '+message.data.status+' — '+message.data.results.length+' results',message.data.status==='completed'?'ok':'bad')}}catch(e){line('Invalid live event','bad')}};eventStream.onerror=()=>line('Live event stream reconnecting…');
loadStatus().then(()=>line('Helper interface ready','ok')).catch(e=>line(e.message,'bad'));
setInterval(()=>{if(document.visibilityState==='visible')refreshStatusSilently()},5000);
</script></body></html>`

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" { http.NotFound(w,r); return }
	w.Header().Set("Content-Type","text/html; charset=utf-8")
	_, _ = io.WriteString(w,page)
}

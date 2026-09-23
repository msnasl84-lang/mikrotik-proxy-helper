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
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const version = "0.1.0"

type Profile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Scheme       string `json:"scheme"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	UUID         string `json:"uuid,omitempty"`
	Type         string `json:"type,omitempty"`
	Security     string `json:"security,omitempty"`
	HeaderType   string `json:"header_type,omitempty"`
	Path         string `json:"path,omitempty"`
	SNI          string `json:"sni,omitempty"`
	RequiredCore string `json:"required_core"`
	Supported    bool   `json:"supported"`
	Reason       string `json:"reason,omitempty"`
	Raw          string `json:"-"`
}

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
}

type App struct {
	mu       sync.RWMutex
	cfg      Config
	state    State
	profiles []Profile
	client   *http.Client
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
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	app := &App{cfg: cfg, state: State{Mode: "manual-health", Health: Health{Status: "unknown"}}}
	app.client = app.proxyHTTPClient()
	if err := app.loadState(); err != nil {
		log.Printf("state load: %v", err)
	}
	if cfg.SubscriptionURL != "" {
		if err := app.refreshSubscription(context.Background()); err != nil {
			log.Printf("initial subscription refresh: %v", err)
		}
	}
	go app.healthLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.auth(app.handleIndex))
	mux.HandleFunc("/api/status", app.auth(app.handleStatus))
	mux.HandleFunc("/api/refresh", app.auth(app.handleRefresh))
	mux.HandleFunc("/api/select", app.auth(app.handleSelect))
	mux.HandleFunc("/api/health", app.auth(app.handleHealth))
	server := &http.Server{Addr: cfg.ListenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("mikrotik-proxy-helper %s listening on %s", version, cfg.ListenAddr)
	log.Fatal(server.ListenAndServe())
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
		p.Supported = p.Host != "" && p.Port > 0 && p.UUID != "" &&
			(p.Type == "" || p.Type == "tcp" || p.Type == "raw") &&
			(p.Security == "" || p.Security == "none")
		if !p.Supported { p.Reason = "v0.1 supports VLESS raw/TCP with security=none only" }
	} else {
		p.Reason = "protocol detected but not enabled in v0.1"
	}
	identity := strings.Join([]string{p.Scheme, p.UUID, strings.ToLower(p.Host), strconv.Itoa(p.Port), q.Encode()}, "|")
	sum := sha256.Sum256([]byte(identity))
	p.ID = hex.EncodeToString(sum[:8])
	return p
}

func (a *App) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "POST required", http.StatusMethodNotAllowed); return }
	if err := a.refreshSubscription(r.Context()); err != nil { http.Error(w, err.Error(), http.StatusBadGateway); return }
	a.writeStatus(w)
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
	if selected == nil { http.Error(w, "profile not found", http.StatusNotFound); return }
	if !selected.Supported { http.Error(w, selected.Reason, http.StatusUnprocessableEntity); return }
	config := makeXrayConfig(*selected)
	if err := writeJSONAtomic(filepath.Join(a.cfg.XrayConfigDir, "config.pending.json"), config, 0o600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError); return
	}
	request := map[string]any{"action":"apply-profile","profile_id":selected.ID,"created_at":time.Now().UTC()}
	if err := writeJSONAtomic(filepath.Join(a.cfg.DataDir, "apply-request.json"), request, 0o600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError); return
	}
	a.state.SelectedProfile = selected.ID
	_ = a.saveStateLocked()
	a.writeStatusLocked(w)
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

func (a *App) healthLoop() {
	ticker := time.NewTicker(a.cfg.HealthInterval); defer ticker.Stop()
	for range ticker.C { ctx,cancel:=context.WithTimeout(context.Background(),a.cfg.HealthTimeout); a.runHealth(ctx); cancel() }
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w,"POST required",http.StatusMethodNotAllowed); return }
	ctx,cancel:=context.WithTimeout(r.Context(),a.cfg.HealthTimeout); defer cancel(); h:=a.runHealth(ctx)
	w.Header().Set("Content-Type","application/json"); json.NewEncoder(w).Encode(h)
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) { a.writeStatus(w) }
func (a *App) writeStatus(w http.ResponseWriter) { a.mu.RLock(); defer a.mu.RUnlock(); a.writeStatusLocked(w) }
func (a *App) writeStatusLocked(w http.ResponseWriter) {
	w.Header().Set("Content-Type","application/json")
	json.NewEncoder(w).Encode(map[string]any{"version":version,"state":a.state,"profiles":a.profiles})
}

var page = template.Must(template.New("index").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>MikroTik Proxy Helper</title><style>body{font-family:sans-serif;max-width:1000px;margin:2rem auto;padding:0 1rem;background:#111;color:#eee}table{width:100%;border-collapse:collapse}th,td{padding:.6rem;border-bottom:1px solid #444;text-align:left}button{padding:.5rem .8rem}code{color:#9fe}</style></head><body><h1>MikroTik Proxy Helper</h1><p>Mode: <code>{{.State.Mode}}</code> — Health: <code>{{.State.Health.Status}}</code></p><form method="post" action="/api/refresh"><button>Refresh subscription</button></form><h2>Profiles</h2><table><tr><th>Name</th><th>Endpoint</th><th>Protocol</th><th>Supported</th><th>Action</th></tr>{{range .Profiles}}<tr><td>{{.Name}}</td><td>{{.Host}}:{{.Port}}</td><td>{{.Scheme}}/{{.Type}}</td><td>{{.Supported}} {{.Reason}}</td><td><form method="post" action="/api/select"><input type="hidden" name="id" value="{{.ID}}"><button {{if not .Supported}}disabled{{end}}>Prepare</button></form></td></tr>{{end}}</table><form method="post" action="/api/health"><button>Test active tunnel</button></form></body></html>`))

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" { http.NotFound(w,r); return }
	a.mu.RLock(); defer a.mu.RUnlock()
	if err := page.Execute(w,map[string]any{"State":a.state,"Profiles":a.profiles}); err != nil { log.Printf("template: %v",err) }
}

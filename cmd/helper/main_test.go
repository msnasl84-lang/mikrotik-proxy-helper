package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/mikrotik-proxy-helper/internal/events"
	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
	"github.com/OWNER/mikrotik-proxy-helper/internal/results"
)

func TestParseProfileStableIDIgnoresDisplayName(t *testing.T) {
	a := parseProfile("vless://00000000-0000-0000-0000-000000000001@example.com:443?type=tcp&security=none#first")
	b := parseProfile("vless://00000000-0000-0000-0000-000000000001@example.com:443?type=tcp&security=none#renamed")
	if !a.Supported { t.Fatalf("profile unexpectedly unsupported: %s", a.Reason) }
	if a.ID != b.ID { t.Fatalf("display name changed stable ID: %s != %s", a.ID, b.ID) }
}

func TestInvalidUUIDIsUnsupported(t *testing.T) {
	p := parseProfile("vless://not-a-uuid@example.com:443?type=tcp&security=none")
	if p.Supported { t.Fatal("invalid UUID was accepted") }
}

func TestProfileJSONDoesNotExposeUUIDOrRawLink(t *testing.T) {
	p := parseProfile("vless://00000000-0000-0000-0000-000000000001@example.com:443?type=tcp&security=none")
	b, err := json.Marshal(p)
	if err != nil { t.Fatal(err) }
	text := string(b)
	if strings.Contains(text, p.UUID) || strings.Contains(text, "vless://") { t.Fatalf("profile JSON leaked credentials: %s", text) }
}

func TestPageRefreshesSingleTestStatusAndShowsFailureDetails(t *testing.T) {
	if !strings.Contains(page, "finally{await loadStatus()}") {
		t.Fatal("single-profile test does not refresh status")
	}
	if !strings.Contains(page, "details.error_code") || !strings.Contains(page, "details.error_message") {
		t.Fatal("test diagnostics are not rendered")
	}
	if !strings.Contains(page, "Latency (TTFB)") || !strings.Contains(page, " · Best") {
		t.Fatal("per-profile latency ranking is not rendered")
	}
}

func TestPageSeparatesPreparedAndActiveProfiles(t *testing.T) {
	for _, required := range []string{"Prepared", "Activate", "Active", "/api/activate"} {
		if !strings.Contains(page, required) { t.Fatalf("page is missing %q", required) }
	}
}

func TestPageProvidesRouterModes(t *testing.T) {
	for _, mode := range []string{`data-mode="vless"`, `data-mode="blocked"`, `data-mode="direct"`} {
		if !strings.Contains(page, mode) { t.Fatalf("page is missing %q", mode) }
	}
	for _, forbidden := range []string{"ovpn", "OVPN", "OpenVPN"} {
		if strings.Contains(page, forbidden) { t.Fatalf("page must not contain %q", forbidden) }
	}
}

func TestPageWaitsForRouterModeAndRefreshesInBackground(t *testing.T) {
	for _, required := range []string{"waitForMode(mode)", "(data.router||{}).mode===mode", "setInterval", "refreshStatusSilently"} {
		if !strings.Contains(page, required) { t.Fatalf("page is missing mode refresh behavior %q", required) }
	}
}

func TestStatusHidesStaleHealthWhileModeIsBlocked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "router-status.json"), []byte(`{"mode":"blocked"}`), 0o600); err != nil { t.Fatal(err) }
	app := &App{cfg: Config{DataDir:dir}, state: State{Health:Health{Status:"healthy", LatencyMS:100}}}
	w := httptest.NewRecorder()
	app.writeStatus(w)
	var response struct {
		State State `json:"state"`
		Profiles []Profile `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil { t.Fatal(err) }
	if response.State.Health.Status != "inactive" || response.State.Health.LatencyMS != 0 {
		t.Fatalf("stale health shown in blocked mode: %+v", response.State.Health)
	}
	if response.Profiles == nil { t.Fatal("empty profiles must serialize as an array") }
}

func TestRestoreLastTestsUsesNewestResultPerProfile(t *testing.T) {
	runs := []results.Run{
		{Results: []model.TestResult{{ProfileID: "a", Status: "timeout"}, {ProfileID: "b", Status: "healthy", TTFBMS: 300}}},
		{Results: []model.TestResult{{ProfileID: "a", Status: "healthy", TTFBMS: 120}}},
	}
	last := restoreLastTests(runs)
	if last["a"].Status != "healthy" || last["a"].TTFBMS != 120 { t.Fatalf("unexpected profile a: %+v", last["a"]) }
	if last["b"].TTFBMS != 300 { t.Fatalf("unexpected profile b: %+v", last["b"]) }
}

func TestEventStreamClosesWhenAppStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	app := &App{ctx: ctx, events: events.New()}
	server := httptest.NewServer(http.HandlerFunc(app.handleEvents))
	defer server.Close()

	response, err := server.Client().Get(server.URL)
	if err != nil { t.Fatal(err) }
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK { t.Fatalf("unexpected status: %d", response.StatusCode) }

	initial := make([]byte, len(": connected\n\n"))
	if _, err := io.ReadFull(response.Body, initial); err != nil { t.Fatalf("read initial event: %v", err) }

	cancel()
	closed := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1)
		_, err := response.Body.Read(buffer)
		closed <- err
	}()
	select {
	case err := <-closed:
		if err == nil { t.Fatal("SSE stream remained readable after app shutdown") }
	case <-time.After(time.Second):
		t.Fatal("SSE stream did not close when app stopped")
	}
}

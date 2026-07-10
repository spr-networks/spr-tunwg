package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeStubTunwg writes a shell script standing in for the tunwg binary.
func writeStubTunwg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tunwg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWaitStartupFailureIsDiagnosed(t *testing.T) {
	stub := writeStubTunwg(t, `echo "tunwg: initiating handshake to server"
echo 'failed to connect: Post "https://l.tunwg.com/add": dial tcp: lookup l.tunwg.com: i/o timeout' >&2
exit 1`)
	m := NewManager(stub, t.TempDir())
	defer m.StopAll()
	m.Start(Forward{Name: "cam", LocalURL: "http://192.168.0.1", Enabled: true}, "", "")

	st, err := m.WaitStartup("cam", 5*time.Second)
	if err == nil {
		t.Fatal("expected a startup error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "waiting for tunnel: Can't reach the tunwg relay") {
		t.Errorf("error = %q, want the diagnosed reason, not a bare exit status", msg)
	}
	if !strings.Contains(msg, "custom relay domain") {
		t.Errorf("error must embed the hint, got %q", msg)
	}
	if !strings.Contains(msg, "failed to connect") {
		t.Errorf("error must embed the last log lines, got %q", msg)
	}
	if st.LastExitCode != 1 {
		t.Errorf("LastExitCode = %d, want 1", st.LastExitCode)
	}
	if st.LastErrorReason != "Can't reach the tunwg relay" || st.LastErrorHint == "" {
		t.Errorf("status not diagnosed: %+v", st)
	}
	if len(st.LastLog) == 0 || !strings.Contains(strings.Join(st.LastLog, "\n"), "i/o timeout") {
		t.Errorf("LastLog missing the child output: %v", st.LastLog)
	}

	// The ring buffer (Log endpoint source) holds both stdout and stderr.
	joined := strings.Join(m.Log("cam"), "\n")
	if !strings.Contains(joined, "initiating handshake") || !strings.Contains(joined, "i/o timeout") {
		t.Errorf("Log() missing combined output: %q", joined)
	}
}

func TestWaitStartupSuccess(t *testing.T) {
	stub := writeStubTunwg(t, `echo "tunwg: http://192.168.0.1 <= https://abcd1234.l.tunwg.com"
sleep 60`)
	m := NewManager(stub, t.TempDir())
	defer m.StopAll()
	m.Start(Forward{Name: "ha", LocalURL: "http://192.168.0.1", Enabled: true}, "", "")

	st, err := m.WaitStartup("ha", 5*time.Second)
	if err != nil {
		t.Fatalf("WaitStartup: %v", err)
	}
	if st.PublicURL != "https://abcd1234.l.tunwg.com" {
		t.Errorf("PublicURL = %q", st.PublicURL)
	}
	if st.LastErrorReason != "" || len(st.LastLog) != 0 {
		t.Errorf("running forward must not carry failure fields: %+v", st)
	}
}

func TestWaitStartupTimeout(t *testing.T) {
	stub := writeStubTunwg(t, "sleep 60")
	m := NewManager(stub, t.TempDir())
	defer m.StopAll()
	m.Start(Forward{Name: "slow", LocalURL: "http://192.168.0.1", Enabled: true}, "", "")

	_, err := m.WaitStartup("slow", 700*time.Millisecond)
	if err == nil || !strings.HasPrefix(err.Error(), "waiting for tunnel: no public URL") {
		t.Errorf("expected a timeout error, got %v", err)
	}
}

// withTestGlobals swaps gConfig/gManager for a handler test and restores them.
func withTestGlobals(t *testing.T, cfg Config, m *Manager) {
	t.Helper()
	prevCfg, prevMgr := gConfig, gManager
	gConfig, gManager = cfg, m
	t.Cleanup(func() {
		gConfig, gManager = prevCfg, prevMgr
	})
}

// failedProc injects a supervised-but-crashed child into a Manager, the state
// a forward is in after tunwg exits and before the next backoff restart.
func failedProc(m *Manager, name string, st ForwardStatus, logLines []string) {
	p := &proc{cancel: func() {}, done: make(chan struct{}), ring: newLogRing()}
	for _, l := range logLines {
		p.ring.Append(l)
	}
	p.status = st
	m.procs[name] = p
}

func TestForwardsJSONIncludesDiagnosis(t *testing.T) {
	m := NewManager("/bin/false", t.TempDir())
	failedProc(m, "cam", ForwardStatus{
		LastError:       "exit status 1",
		LastExitCode:    1,
		LastErrorReason: "Can't reach the tunwg relay",
		LastErrorHint:   "Check WAN/DNS from the plugin container.",
		LastLog:         []string{"failed to connect: dial tcp: lookup l.tunwg.com: i/o timeout"},
		Restarts:        3,
	}, []string{"failed to connect: dial tcp: lookup l.tunwg.com: i/o timeout"})
	withTestGlobals(t, Config{Forwards: []Forward{
		{Name: "cam", LocalURL: "http://192.168.0.1", Enabled: true},
	}}, m)

	rec := httptest.NewRecorder()
	handleGetForwards(rec, httptest.NewRequest("GET", "/forwards", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	st, ok := rows[0]["Status"].(map[string]any)
	if !ok {
		t.Fatalf("missing Status: %v", rows[0])
	}
	if st["LastExitCode"] != float64(1) {
		t.Errorf("LastExitCode = %v", st["LastExitCode"])
	}
	if st["LastErrorReason"] != "Can't reach the tunwg relay" {
		t.Errorf("LastErrorReason = %v", st["LastErrorReason"])
	}
	if st["LastErrorHint"] != "Check WAN/DNS from the plugin container." {
		t.Errorf("LastErrorHint = %v", st["LastErrorHint"])
	}
	lastLog, ok := st["LastLog"].([]any)
	if !ok || len(lastLog) != 1 {
		t.Errorf("LastLog = %v", st["LastLog"])
	}
}

func TestForwardLogEndpoint(t *testing.T) {
	m := NewManager("/bin/false", t.TempDir())
	failedProc(m, "cam", ForwardStatus{LastExitCode: 1}, []string{
		"tunwg: initiating handshake to server",
		"failed to connect: error adding peer: 403 Forbidden",
	})
	withTestGlobals(t, Config{Forwards: []Forward{
		{Name: "cam", LocalURL: "http://192.168.0.1", Enabled: true},
	}}, m)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /forwards/{name}/log", handleGetForwardLog)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/forwards/cam/log", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct{ Lines []string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Lines) != 2 || !strings.Contains(body.Lines[1], "403 Forbidden") {
		t.Errorf("Lines = %v", body.Lines)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/forwards/nope/log", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown forward: status = %d, want 404", rec.Code)
	}
}

func TestForwardCredentialsEndpoint(t *testing.T) {
	m := NewManager("/bin/false", t.TempDir())
	withTestGlobals(t, Config{Forwards: []Forward{
		{Name: "cam", LocalURL: "http://192.168.0.1", Auth: "alice:$2y$10$hash", AuthPassword: "correct horse"},
		{Name: "legacy", LocalURL: "http://192.168.0.2", Auth: "bob:$2y$10$hash"},
	}}, m)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /forwards/{name}/credentials", handleGetForwardCredentials)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/forwards/cam/credentials", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var saved forwardCredentialsView
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Username != "alice" || saved.Password != "correct horse" || !saved.PasswordAvailable {
		t.Errorf("credentials = %+v", saved)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/forwards/legacy/credentials", nil))
	var legacy forwardCredentialsView
	if err := json.Unmarshal(rec.Body.Bytes(), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Username != "bob" || legacy.Password != "" || legacy.PasswordAvailable {
		t.Errorf("legacy credentials = %+v", legacy)
	}
}

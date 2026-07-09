package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestValidateLocalURL(t *testing.T) {
	valid := []string{
		"http://192.168.2.50:8123",
		"http://192.168.2.50:8123/",
		"http://10.0.0.5",
		"https://172.16.1.2:8443",
		"http://[fd00::1]:8080", // IPv6 ULA
	}
	for _, u := range valid {
		if err := validateLocalURL(u); err != nil {
			t.Errorf("expected %q to be valid, got: %v", u, err)
		}
	}

	invalid := map[string]string{
		"":                                "empty",
		"http://localhost:8080":           "hostname, not an IP",
		"http://myserver.lan:8080":        "hostname, not an IP",
		"http://127.0.0.1:8080":           "loopback",
		"http://[::1]:8080":               "IPv6 loopback",
		"http://0.0.0.0:80":               "unspecified",
		"http://8.8.8.8:80":               "public IP",
		"http://1.1.1.1":                  "public IP no port",
		"http://169.254.1.1:80":           "link-local",
		"http://224.0.0.1:80":             "multicast",
		"ftp://192.168.1.2:21":            "bad scheme",
		"192.168.1.2:8080":                "missing scheme",
		"http://192.168.1.2:0":            "port 0",
		"http://192.168.1.2:70000":        "port out of range",
		"http://192.168.1.2:8080/admin":   "path not allowed",
		"http://192.168.1.2:8080?x=1":     "query not allowed",
		"http://192.168.1.2:8080#frag":    "fragment not allowed",
		"http://user:pass@192.168.1.2:80": "credentials not allowed",
		"http://172.32.0.1:80":            "just outside 172.16/12",
	}
	for u, why := range invalid {
		if err := validateLocalURL(u); err == nil {
			t.Errorf("expected %q to be rejected (%s)", u, why)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, name := range []string{"ha", "home-assistant", "cam1", "x", "a1-b2-c3"} {
		if err := validateName(name); err != nil {
			t.Errorf("expected name %q to be valid, got: %v", name, err)
		}
	}
	for _, name := range []string{"", "-lead", "trail-", "UPPER", "with space", "dot.name",
		"semi;colon", "$(cmd)", strings.Repeat("a", 33), "a/b", "../etc"} {
		if err := validateName(name); err == nil {
			t.Errorf("expected name %q to be rejected", name)
		}
	}
}

func TestValidateKey(t *testing.T) {
	for _, key := range []string{"", "mykey", "my-key_2.suffix", "A1"} {
		if err := validateKey(key); err != nil {
			t.Errorf("expected key %q to be valid, got: %v", key, err)
		}
	}
	for _, key := range []string{".", "..", ".hidden", "-lead", "a/b", "../x",
		"with space", strings.Repeat("k", 65)} {
		if err := validateKey(key); err == nil {
			t.Errorf("expected key %q to be rejected", key)
		}
	}
}

func TestValidateAuth(t *testing.T) {
	for _, auth := range []string{"", "user:$2y$05$abcdefghijklmnopqrstuv", "bob:plainpass"} {
		if err := validateAuth(auth); err != nil {
			t.Errorf("expected auth %q to be valid, got: %v", auth, err)
		}
	}
	for _, auth := range []string{"nocolon", "user:", ":hash", "bad user:hash",
		"user:has space", "user:" + strings.Repeat("h", 129)} {
		if err := validateAuth(auth); err == nil {
			t.Errorf("expected auth %q to be rejected", auth)
		}
	}
}

func TestValidateAPIDomainAndToken(t *testing.T) {
	for _, d := range []string{"", "l.tunwg.com", "tunnel.example.org"} {
		if err := validateAPIDomain(d); err != nil {
			t.Errorf("expected domain %q to be valid, got: %v", d, err)
		}
	}
	for _, d := range []string{"nodots", "UPPER.example.com", "-x.example.com",
		"exa mple.com", "a..b"} {
		if err := validateAPIDomain(d); err == nil {
			t.Errorf("expected domain %q to be rejected", d)
		}
	}
	if err := validateAuthToken("secret-token_123"); err != nil {
		t.Errorf("expected token to be valid, got: %v", err)
	}
	for _, tok := range []string{"has space", strings.Repeat("t", 129), "ctrl\x01char"} {
		if err := validateAuthToken(tok); err == nil {
			t.Errorf("expected token %q to be rejected", tok)
		}
	}
}

func TestParsePublicURL(t *testing.T) {
	line := "2026/07/09 10:00:00 tunwg: http://192.168.2.50:8123 <= https://abc123xyz.l.tunwg.com"
	if got := parsePublicURL(line); got != "https://abc123xyz.l.tunwg.com" {
		t.Errorf("parsePublicURL = %q", got)
	}
	for _, l := range []string{"", "tunwg: initiating handshake to server",
		"GET / 200", "https://not-an-announcement.example.com"} {
		if got := parsePublicURL(l); got != "" {
			t.Errorf("expected no URL from %q, got %q", l, got)
		}
	}
}

func TestTunwgArgsAndEnv(t *testing.T) {
	f := Forward{Name: "ha", LocalURL: "http://192.168.2.50:8123", Enabled: true}
	args := tunwgArgs(f)
	if !slices.Equal(args, []string{"--forward=http://192.168.2.50:8123"}) {
		t.Errorf("unexpected args: %v", args)
	}
	f.Auth = "user:hash"
	if args := tunwgArgs(f); !slices.Contains(args, "--limit=user:hash") {
		t.Errorf("expected --limit arg, got %v", args)
	}

	env := tunwgEnv(f, "", "", "/keys")
	if !slices.Contains(env, "TUNWG_KEY=ha") {
		t.Errorf("expected TUNWG_KEY to default to forward name, got %v", env)
	}
	if !slices.Contains(env, "TUNWG_PATH=/keys") {
		t.Errorf("expected TUNWG_PATH, got %v", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "TUNWG_RELAY") || strings.HasPrefix(e, "TUNWG_API") ||
			strings.HasPrefix(e, "TUNWG_AUTH") {
			t.Errorf("unexpected env entry %q", e)
		}
	}

	f.Key = "custom"
	f.Relay = true
	env = tunwgEnv(f, "tunnel.example.org", "tok", "/keys")
	for _, want := range []string{"TUNWG_KEY=custom", "TUNWG_RELAY=true",
		"TUNWG_API=tunnel.example.org", "TUNWG_AUTH=tok"} {
		if !slices.Contains(env, want) {
			t.Errorf("expected %q in env %v", want, env)
		}
	}
}

func TestForwardViewRedactsSecrets(t *testing.T) {
	f := Forward{Name: "ha", LocalURL: "http://192.168.2.50:8123",
		Key: "supersecret", Auth: "user:hash", Enabled: true}
	data, err := json.Marshal(forwardView(f, ForwardStatus{}))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "supersecret") || strings.Contains(s, "user:hash") {
		t.Errorf("secrets leaked in view: %s", s)
	}
	if !strings.Contains(s, `"KeyConfigured":true`) || !strings.Contains(s, `"AuthConfigured":true`) {
		t.Errorf("expected configured flags in view: %s", s)
	}
}

func TestConfigWriteLoadRoundtrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix perms")
	}
	dir := t.TempDir()
	origConfig := ConfigFile
	ConfigFile = filepath.Join(dir, "config.json")
	defer func() { ConfigFile = origConfig }()

	configMtx.Lock()
	gConfig = Config{
		APIDomain: "tunnel.example.org",
		AuthToken: "tok",
		Forwards: []Forward{
			{Name: "ha", LocalURL: "http://192.168.2.50:8123", Key: "k", Enabled: true},
		},
	}
	err := writeConfigLocked()
	configMtx.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("config file perms = %o, want 0600", perm)
	}

	configMtx.Lock()
	gConfig = Config{}
	configMtx.Unlock()
	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	configMtx.Lock()
	defer configMtx.Unlock()
	if gConfig.APIDomain != "tunnel.example.org" || gConfig.AuthToken != "tok" ||
		len(gConfig.Forwards) != 1 || gConfig.Forwards[0].Name != "ha" ||
		gConfig.Forwards[0].Key != "k" {
		t.Errorf("roundtrip mismatch: %+v", gConfig)
	}
}

func TestLoadConfigMissingFileOK(t *testing.T) {
	dir := t.TempDir()
	origConfig := ConfigFile
	ConfigFile = filepath.Join(dir, "nope", "config.json")
	defer func() { ConfigFile = origConfig }()

	configMtx.Lock()
	gConfig = Config{Forwards: []Forward{}}
	configMtx.Unlock()
	if err := loadConfig(); err != nil {
		t.Errorf("missing config should not error, got: %v", err)
	}
}

func TestValidateForward(t *testing.T) {
	good := Forward{Name: "ha", LocalURL: "http://192.168.2.50:8123"}
	if err := validateForward(good); err != nil {
		t.Errorf("expected valid forward, got: %v", err)
	}
	bad := []Forward{
		{Name: "", LocalURL: "http://192.168.2.50:8123"},
		{Name: "ha", LocalURL: "http://localhost:8123"},
		{Name: "ha", LocalURL: "http://192.168.2.50:8123", Key: "../escape"},
		{Name: "ha", LocalURL: "http://192.168.2.50:8123", Auth: "nocolon"},
	}
	for _, f := range bad {
		if err := validateForward(f); err == nil {
			t.Errorf("expected forward %+v to be rejected", f)
		}
	}
}

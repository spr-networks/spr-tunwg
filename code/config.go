package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
)

// TEST_PREFIX lets unit tests point file paths at a scratch directory.
var TEST_PREFIX = os.Getenv("TEST_PREFIX")

var (
	ConfigFile     = TEST_PREFIX + "/configs/spr-tunwg/config.json"
	KeyStoragePath = TEST_PREFIX + "/state/plugins/spr-tunwg/tunwg"
	StateHomeDir   = TEST_PREFIX + "/state/plugins/spr-tunwg"
)

// DefaultAPIDomain is upstream tunwg's public relay.
const DefaultAPIDomain = "l.tunwg.com"

// Forward is one public tunnel: a tunwg child process forwarding a public
// HTTPS URL to a local (LAN) http(s) service.
type Forward struct {
	Name     string
	LocalURL string // --forward target, e.g. http://192.168.2.50:8123
	Key      string `json:",omitempty"` // TUNWG_KEY name (stable subdomain); defaults to Name. Secret-adjacent: never echoed back.
	Auth     string `json:",omitempty"` // optional --limit "user:bcrypt-hash" basic auth. Never echoed back.
	Relay    bool   // TUNWG_RELAY=true (tunnel WireGuard over HTTPS when UDP is blocked)
	Enabled  bool
}

// Config is persisted at /configs/spr-tunwg/config.json (0600).
type Config struct {
	APIDomain string `json:",omitempty"` // optional TUNWG_API (self-hosted tunwg server)
	AuthToken string `json:",omitempty"` // optional TUNWG_AUTH for a self-hosted server. Never echoed back.
	Forwards  []Forward
}

var (
	configMtx sync.Mutex
	gConfig   = Config{Forwards: []Forward{}}
)

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	keyRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	authUserRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	domainRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})?(\.[a-z0-9]([a-z0-9-]{0,62})?)+$`)
)

func validateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name: use 1-32 lowercase letters, digits or '-', starting and ending alphanumeric")
	}
	return nil
}

// validateLocalURL enforces that a forward target is an http(s) URL whose
// host is a private (RFC1918 / IPv6 ULA) LAN address with a sane port.
// Loopback, link-local, unspecified, multicast and public addresses are
// rejected: the tunnel is meant to expose LAN services, deliberately.
func validateLocalURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("missing LocalURL")
	}
	if len(raw) > 256 {
		return fmt.Errorf("LocalURL too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid LocalURL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("LocalURL must use http:// or https://")
	}
	if u.User != nil {
		return fmt.Errorf("LocalURL must not contain credentials")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("LocalURL must not contain a path, query or fragment (tunwg forwards the whole host)")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("LocalURL is missing a host")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("LocalURL host must be a literal LAN IP address (got %q)", host)
	}
	if ip.IsLoopback() {
		return fmt.Errorf("loopback targets are not allowed: nothing useful runs on the plugin container's localhost")
	}
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("LocalURL host must be a private LAN address")
	}
	if !ip.IsPrivate() {
		return fmt.Errorf("LocalURL host must be a private LAN address (RFC1918), refusing to proxy %q", host)
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid port %q", p)
		}
	}
	return nil
}

func validateKey(key string) error {
	if key == "" {
		return nil // defaults to the forward name
	}
	if !keyRe.MatchString(key) {
		return fmt.Errorf("invalid Key: use 1-64 letters, digits, '.', '_' or '-', starting alphanumeric")
	}
	return nil
}

// validateAuth checks the htpasswd-style "user:hash" value passed to tunwg
// --limit. It is passed as a single argv element (never through a shell).
func validateAuth(auth string) error {
	if auth == "" {
		return nil
	}
	user, hash, found := cutAuth(auth)
	if !found {
		return fmt.Errorf("Auth must be in htpasswd format user:hash")
	}
	if !authUserRe.MatchString(user) {
		return fmt.Errorf("invalid Auth username")
	}
	if len(hash) < 1 || len(hash) > 128 {
		return fmt.Errorf("invalid Auth hash length")
	}
	for _, c := range hash {
		if c <= ' ' || c > '~' {
			return fmt.Errorf("Auth hash contains invalid characters")
		}
	}
	return nil
}

func cutAuth(auth string) (string, string, bool) {
	for i := 0; i < len(auth); i++ {
		if auth[i] == ':' {
			return auth[:i], auth[i+1:], true
		}
	}
	return auth, "", false
}

func validateAPIDomain(domain string) error {
	if domain == "" {
		return nil
	}
	if len(domain) > 253 || !domainRe.MatchString(domain) {
		return fmt.Errorf("invalid APIDomain")
	}
	return nil
}

func validateAuthToken(token string) error {
	if token == "" {
		return nil
	}
	if len(token) > 128 {
		return fmt.Errorf("AuthToken too long")
	}
	for _, c := range token {
		if c <= ' ' || c > '~' {
			return fmt.Errorf("AuthToken contains invalid characters")
		}
	}
	return nil
}

func validateForward(f Forward) error {
	if err := validateName(f.Name); err != nil {
		return err
	}
	if err := validateLocalURL(f.LocalURL); err != nil {
		return err
	}
	if err := validateKey(f.Key); err != nil {
		return err
	}
	return validateAuth(f.Auth)
}

func loadConfig() error {
	configMtx.Lock()
	defer configMtx.Unlock()
	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cfg := Config{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	if cfg.Forwards == nil {
		cfg.Forwards = []Forward{}
	}
	gConfig = cfg
	return nil
}

// writeConfigLocked persists the config atomically (tmp+rename, 0600).
// Caller must hold configMtx.
func writeConfigLocked() error {
	data, err := json.MarshalIndent(gConfig, "", " ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(ConfigFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp := ConfigFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, ConfigFile)
}

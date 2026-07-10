package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

var (
	UNIX_PLUGIN_LISTENER = TEST_PREFIX + "/state/plugins/spr-tunwg/socket"
	TunwgBin             = "/usr/local/bin/tunwg"
	UIPath               = "/ui"
)

var gManager *Manager

// ForwardView is the list representation of a forward. Key, the bcrypt Auth
// value and the recoverable password are omitted; credentials are returned
// only by the explicit per-forward credentials endpoint.
type ForwardView struct {
	Name           string
	LocalURL       string
	KeyConfigured  bool
	AuthConfigured bool
	Relay          bool
	Enabled        bool
	Status         ForwardStatus
}

func forwardView(f Forward, st ForwardStatus) ForwardView {
	return ForwardView{
		Name:           f.Name,
		LocalURL:       f.LocalURL,
		KeyConfigured:  f.Key != "",
		AuthConfigured: f.Auth != "",
		Relay:          f.Relay,
		Enabled:        f.Enabled,
		Status:         st,
	}
}

type forwardCredentialsView struct {
	Username          string
	Password          string `json:",omitempty"`
	PasswordAvailable bool
}

func credentialsView(f Forward) forwardCredentialsView {
	username, _, _ := cutAuth(f.Auth)
	return forwardCredentialsView{
		Username:          username,
		Password:          f.AuthPassword,
		PasswordAvailable: f.AuthPassword != "",
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Println("encode failed:", err)
	}
}

func effectiveAPIDomain(cfg Config) string {
	if cfg.APIDomain != "" {
		return cfg.APIDomain
	}
	return DefaultAPIDomain
}

type StatusView struct {
	Running         bool
	TunwgVersion    string
	APIDomain       string
	ForwardsTotal   int
	ForwardsEnabled int
	ForwardsRunning int
}

func handleGetStatus(w http.ResponseWriter, r *http.Request) {
	configMtx.Lock()
	defer configMtx.Unlock()

	view := StatusView{
		Running:      true,
		TunwgVersion: os.Getenv("TUNWG_VERSION"),
		APIDomain:    effectiveAPIDomain(gConfig),
	}
	for _, f := range gConfig.Forwards {
		view.ForwardsTotal++
		if f.Enabled {
			view.ForwardsEnabled++
		}
		st := gManager.Status(f.Name)
		if st.Running && st.PublicURL != "" {
			view.ForwardsRunning++
		}
	}
	writeJSON(w, view)
}

func handleGetForwards(w http.ResponseWriter, r *http.Request) {
	configMtx.Lock()
	defer configMtx.Unlock()

	views := []ForwardView{}
	for _, f := range gConfig.Forwards {
		views = append(views, forwardView(f, gManager.Status(f.Name)))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	writeJSON(w, views)
}

// startupWait is how long POST /forwards waits for a newly enabled forward
// to either announce its public URL or fail with a diagnosable error.
const startupWait = 10 * time.Second

// addForwardResponse is the POST /forwards reply: the created forward, plus
// a diagnosed "waiting for tunnel" message when the tunnel did not come up
// within startupWait. The forward stays configured either way (the
// supervisor keeps retrying with backoff).
type addForwardResponse struct {
	ForwardView
	StartupError string `json:",omitempty"`
}

func handleAddForward(w http.ResponseWriter, r *http.Request) {
	f := Forward{}
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateForward(f); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	configMtx.Lock()
	for _, existing := range gConfig.Forwards {
		if existing.Name == f.Name {
			configMtx.Unlock()
			http.Error(w, "a forward with that name already exists", http.StatusConflict)
			return
		}
	}
	gConfig.Forwards = append(gConfig.Forwards, f)
	if err := writeConfigLocked(); err != nil {
		gConfig.Forwards = gConfig.Forwards[:len(gConfig.Forwards)-1]
		configMtx.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if f.Enabled {
		gManager.Start(f, gConfig.APIDomain, gConfig.AuthToken)
	}
	configMtx.Unlock()

	resp := addForwardResponse{}
	if f.Enabled {
		// Wait (without holding configMtx) so a startup failure comes back
		// with its diagnosis instead of a bare exit status.
		st, err := gManager.WaitStartup(f.Name, startupWait)
		resp.ForwardView = forwardView(f, st)
		if err != nil {
			log.Printf("[%s] %v", f.Name, err)
			resp.StartupError = err.Error()
		}
	} else {
		resp.ForwardView = forwardView(f, gManager.Status(f.Name))
	}
	writeJSON(w, resp)
}

// handleGetForwardLog returns the buffered output of a forward's current or
// most recent tunwg run as {"Lines": [...]}.
func handleGetForwardLog(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	configMtx.Lock()
	found := false
	for _, f := range gConfig.Forwards {
		if f.Name == name {
			found = true
			break
		}
	}
	configMtx.Unlock()

	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, struct{ Lines []string }{Lines: gManager.Log(name)})
}

// handleGetForwardCredentials reveals the credentials only after an explicit
// request from the administrator UI. Older forwards have a recoverable
// username (from Auth) but no password because previous versions stored only
// the bcrypt hash.
func handleGetForwardCredentials(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	configMtx.Lock()
	defer configMtx.Unlock()
	for _, f := range gConfig.Forwards {
		if f.Name != name {
			continue
		}
		if f.Auth == "" {
			http.Error(w, "basic auth is not configured", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, credentialsView(f))
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func handleDeleteForward(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	configMtx.Lock()
	defer configMtx.Unlock()

	for idx, f := range gConfig.Forwards {
		if f.Name == name {
			gConfig.Forwards = append(gConfig.Forwards[:idx], gConfig.Forwards[idx+1:]...)
			if err := writeConfigLocked(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			gManager.Stop(name)
			writeJSON(w, map[string]bool{"Deleted": true})
			return
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func handleToggleForward(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	configMtx.Lock()
	defer configMtx.Unlock()

	for idx, f := range gConfig.Forwards {
		if f.Name == name {
			gConfig.Forwards[idx].Enabled = !f.Enabled
			if err := writeConfigLocked(); err != nil {
				gConfig.Forwards[idx].Enabled = f.Enabled
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if gConfig.Forwards[idx].Enabled {
				gManager.Start(gConfig.Forwards[idx], gConfig.APIDomain, gConfig.AuthToken)
			} else {
				gManager.Stop(name)
			}
			writeJSON(w, forwardView(gConfig.Forwards[idx], gManager.Status(name)))
			return
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// ConfigView is the redacted GET /config response.
type ConfigView struct {
	APIDomain           string
	AuthTokenConfigured bool
}

func handleGetConfig(w http.ResponseWriter, r *http.Request) {
	configMtx.Lock()
	defer configMtx.Unlock()
	writeJSON(w, ConfigView{
		APIDomain:           gConfig.APIDomain,
		AuthTokenConfigured: gConfig.AuthToken != "",
	})
}

type configUpdate struct {
	APIDomain      string
	AuthToken      string // empty = keep existing (use ClearAuthToken to remove)
	ClearAuthToken bool
}

func handlePutConfig(w http.ResponseWriter, r *http.Request) {
	upd := configUpdate{}
	if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateAPIDomain(upd.APIDomain); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateAuthToken(upd.AuthToken); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	configMtx.Lock()
	defer configMtx.Unlock()

	prev := gConfig
	gConfig.APIDomain = upd.APIDomain
	if upd.ClearAuthToken {
		gConfig.AuthToken = ""
	} else if upd.AuthToken != "" {
		gConfig.AuthToken = upd.AuthToken
	}
	if err := writeConfigLocked(); err != nil {
		gConfig = prev
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Relay settings are process environment: restart enabled forwards.
	gManager.StopAll()
	for _, f := range gConfig.Forwards {
		if f.Enabled {
			gManager.Start(f, gConfig.APIDomain, gConfig.AuthToken)
		}
	}
	writeJSON(w, ConfigView{
		APIDomain:           gConfig.APIDomain,
		AuthTokenConfigured: gConfig.AuthToken != "",
	})
}

// spaHandler serves the bundled UI (a single self-contained index.html).
type spaHandler struct {
	staticPath string
	indexPath  string
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, err := filepath.Abs(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path = filepath.Join(h.staticPath, path)
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		http.ServeFile(w, r, filepath.Join(h.staticPath, h.indexPath))
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.FileServer(http.Dir(h.staticPath)).ServeHTTP(w, r)
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("%s %s %s\n", r.RemoteAddr, r.Method, r.URL)
		handler.ServeHTTP(w, r)
	})
}

func main() {
	if err := loadConfig(); err != nil {
		log.Println("failed to load config:", err)
	}

	// Test hook: point at a stub tunwg binary.
	if v := os.Getenv("TUNWG_PLUGIN_BIN"); v != "" {
		TunwgBin = v
	}

	gManager = NewManager(TunwgBin, KeyStoragePath)

	configMtx.Lock()
	for _, f := range gConfig.Forwards {
		if f.Enabled {
			gManager.Start(f, gConfig.APIDomain, gConfig.AuthToken)
		}
	}
	configMtx.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", handleGetStatus)
	mux.HandleFunc("GET /topology", handleGetTopology)
	mux.HandleFunc("GET /forwards", handleGetForwards)
	mux.HandleFunc("POST /forwards", handleAddForward)
	mux.HandleFunc("GET /forwards/{name}/log", handleGetForwardLog)
	mux.HandleFunc("GET /forwards/{name}/credentials", handleGetForwardCredentials)
	mux.HandleFunc("DELETE /forwards/{name}", handleDeleteForward)
	mux.HandleFunc("POST /forwards/{name}/toggle", handleToggleForward)
	mux.HandleFunc("GET /config", handleGetConfig)
	mux.HandleFunc("PUT /config", handlePutConfig)
	mux.Handle("/", spaHandler{staticPath: UIPath, indexPath: "index.html"})

	os.Remove(UNIX_PLUGIN_LISTENER)
	listener, err := net.Listen("unix", UNIX_PLUGIN_LISTENER)
	if err != nil {
		panic(err)
	}
	if err := os.Chmod(UNIX_PLUGIN_LISTENER, 0770); err != nil {
		panic(err)
	}

	server := http.Server{Handler: logRequest(mux)}
	log.Println("spr-tunwg plugin listening on", UNIX_PLUGIN_LISTENER)
	if err := server.Serve(listener); err != nil {
		log.Fatal(err)
	}
}

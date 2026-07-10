package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// publicURLRe matches tunwg's announcement line, e.g.
//
//	tunwg: http://192.168.2.50:8123 <= https://xxxxxxxxxx.l.tunwg.com
var publicURLRe = regexp.MustCompile(`<=\s+(https://\S+)`)

const (
	backoffMin    = 2 * time.Second
	backoffMax    = 60 * time.Second
	stableUptime  = 60 * time.Second
	stopWaitLimit = 10 * time.Second
)

// ForwardStatus is the runtime state of one tunwg child process. The Last*
// fields describe the most recent exit and remain visible across retries;
// they are cleared once a fresh child announces its public URL.
type ForwardStatus struct {
	Running         bool
	PID             int      `json:",omitempty"`
	PublicURL       string   `json:",omitempty"`
	LastError       string   `json:",omitempty"`
	LastExitCode    int      `json:",omitempty"` // -1 when the child died without an exit code
	LastErrorReason string   `json:",omitempty"` // short classification from diagnose()
	LastErrorHint   string   `json:",omitempty"` // what to check, from diagnose()
	LastLog         []string `json:",omitempty"` // sanitized tail of the child's output at exit
	Restarts        int
	StartedAt       time.Time `json:",omitzero"`
}

type proc struct {
	cancel context.CancelFunc
	done   chan struct{}
	ring   *logRing

	mu     sync.Mutex
	status ForwardStatus
}

func (p *proc) snapshot() ForwardStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// Manager supervises one tunwg child process per enabled forward.
type Manager struct {
	tunwgBin string
	keyPath  string

	mu    sync.Mutex
	procs map[string]*proc
}

func NewManager(tunwgBin, keyPath string) *Manager {
	return &Manager{
		tunwgBin: tunwgBin,
		keyPath:  keyPath,
		procs:    map[string]*proc{},
	}
}

// tunwgArgs builds the argv for a forward. Arguments are passed as an argv
// array to exec.Command: no shell is ever involved.
func tunwgArgs(f Forward) []string {
	args := []string{"--forward=" + f.LocalURL}
	if f.Auth != "" {
		args = append(args, "--limit="+f.Auth)
	}
	return args
}

// tunwgEnv builds a minimal environment for a forward's tunwg process.
func tunwgEnv(f Forward, apiDomain, authToken, keyPath string) []string {
	key := f.Key
	if key == "" {
		key = f.Name
	}
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + StateHomeDir,
		"TUNWG_PATH=" + keyPath,
		"TUNWG_KEY=" + key,
	}
	if f.Relay {
		env = append(env, "TUNWG_RELAY=true")
	}
	if apiDomain != "" {
		env = append(env, "TUNWG_API="+apiDomain)
	}
	if authToken != "" {
		env = append(env, "TUNWG_AUTH="+authToken)
	}
	return env
}

// Start launches (or relaunches) the tunwg child for a forward.
func (m *Manager) Start(f Forward, apiDomain, authToken string) {
	m.Stop(f.Name)

	ctx, cancel := context.WithCancel(context.Background())
	p := &proc{cancel: cancel, done: make(chan struct{}), ring: newLogRing()}

	m.mu.Lock()
	m.procs[f.Name] = p
	m.mu.Unlock()

	go m.supervise(ctx, f, apiDomain, authToken, p)
}

// Stop terminates the child for a forward, if running, and waits for it.
func (m *Manager) Stop(name string) {
	m.mu.Lock()
	p := m.procs[name]
	delete(m.procs, name)
	m.mu.Unlock()

	if p == nil {
		return
	}
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(stopWaitLimit):
		log.Printf("[%s] timed out waiting for tunwg to exit", name)
	}
}

// StopAll stops every supervised forward (used on config-wide changes).
func (m *Manager) StopAll() {
	m.mu.Lock()
	names := make([]string, 0, len(m.procs))
	for name := range m.procs {
		names = append(names, name)
	}
	m.mu.Unlock()
	for _, name := range names {
		m.Stop(name)
	}
}

// Status returns the runtime state for a forward name.
func (m *Manager) Status(name string) ForwardStatus {
	m.mu.Lock()
	p := m.procs[name]
	m.mu.Unlock()
	if p == nil {
		return ForwardStatus{}
	}
	return p.snapshot()
}

// Log returns the buffered output (oldest first) of the forward's current or
// most recent tunwg run. Empty when the forward is not supervised.
func (m *Manager) Log(name string) []string {
	m.mu.Lock()
	p := m.procs[name]
	m.mu.Unlock()
	if p == nil {
		return []string{}
	}
	return p.ring.Lines()
}

// startupError composes the "waiting for tunnel" failure message from a
// diagnosed exit: reason, hint and the last few log lines instead of a bare
// exit status.
func startupError(st ForwardStatus) error {
	var b strings.Builder
	fmt.Fprintf(&b, "waiting for tunnel: %s", st.LastErrorReason)
	if st.LastErrorHint != "" {
		fmt.Fprintf(&b, " — %s", st.LastErrorHint)
	}
	if tail := st.LastLog; len(tail) > 0 {
		if len(tail) > 5 {
			tail = tail[len(tail)-5:]
		}
		fmt.Fprintf(&b, "\nlast log lines:\n%s", strings.Join(tail, "\n"))
	}
	return errors.New(b.String())
}

// WaitStartup blocks until the forward announces its public URL, its child
// exits (returning a diagnosed error), or the timeout elapses.
func (m *Manager) WaitStartup(name string, timeout time.Duration) (ForwardStatus, error) {
	deadline := time.Now().Add(timeout)
	for {
		st := m.Status(name)
		switch {
		case st.PublicURL != "":
			return st, nil
		case !st.Running && st.LastErrorReason != "":
			return st, startupError(st)
		case time.Now().After(deadline):
			return st, fmt.Errorf("waiting for tunnel: no public URL after %v — tunwg is still trying to connect; check this forward's log", timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (m *Manager) supervise(ctx context.Context, f Forward, apiDomain, authToken string, p *proc) {
	defer close(p.done)
	backoff := backoffMin
	restarts := 0
	for {
		started := time.Now()
		err := m.runOnce(ctx, f, apiDomain, authToken, p)
		if ctx.Err() != nil {
			p.mu.Lock()
			p.status.Running = false
			p.status.PID = 0
			p.mu.Unlock()
			return
		}
		msg := "tunwg exited"
		exitCode := 0
		if err != nil {
			msg = err.Error()
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		// Persist the tail of the child's output and classify the failure
		// so the API and UI can explain it (not just "exit status 1").
		// The exec error text joins the tail for classification only: it
		// carries the pattern when the child never produced output (e.g.
		// fork/exec permission errors).
		tail := p.ring.Tail(logTailLines)
		reason, hint := diagnose(strings.Join(append(append([]string{}, tail...), msg), "\n"), exitCode)
		restarts++
		p.mu.Lock()
		p.status.Running = false
		p.status.PID = 0
		p.status.PublicURL = ""
		p.status.LastError = msg
		p.status.LastExitCode = exitCode
		p.status.LastErrorReason = reason
		p.status.LastErrorHint = hint
		p.status.LastLog = tail
		p.status.Restarts = restarts
		p.mu.Unlock()

		if time.Since(started) > stableUptime {
			backoff = backoffMin
		} else {
			backoff *= 2
			if backoff > backoffMax {
				backoff = backoffMax
			}
		}
		log.Printf("[%s] tunwg exited (%s: %s), restarting in %v", f.Name, msg, reason, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func (m *Manager) runOnce(ctx context.Context, f Forward, apiDomain, authToken string, p *proc) error {
	cmd := exec.CommandContext(ctx, m.tunwgBin, tunwgArgs(f)...)
	cmd.Env = tunwgEnv(f, apiDomain, authToken, m.keyPath)
	// On stop, SIGTERM the whole process group; WaitDelay hard-kills and
	// reaps if it does not exit in time.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}

	// A fresh run owns the ring buffer: it always describes the current or
	// most recent child, not a mix of attempts.
	p.ring.Reset()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	p.mu.Lock()
	p.status.Running = true
	p.status.PID = cmd.Process.Pid
	p.status.StartedAt = time.Now()
	p.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.scanOutput(f.Name, stdout) }()
	go func() { defer wg.Done(); p.scanOutput(f.Name, stderr) }()
	wg.Wait()
	return cmd.Wait()
}

// scanOutput relays tunwg's log lines to stdout, records them in the
// per-forward ring buffer, and captures the assigned public URL from the
// announcement line.
func (p *proc) scanOutput(name string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		log.Printf("[%s] %s", name, line)
		p.ring.Append(line)
		if u := parsePublicURL(line); u != "" {
			p.mu.Lock()
			p.status.PublicURL = u
			p.status.LastError = ""
			p.status.LastExitCode = 0
			p.status.LastErrorReason = ""
			p.status.LastErrorHint = ""
			p.status.LastLog = nil
			p.mu.Unlock()
		}
	}
}

// parsePublicURL extracts the assigned public URL from a tunwg log line,
// returning "" when the line is not the announcement.
func parsePublicURL(line string) string {
	if match := publicURLRe.FindStringSubmatch(line); match != nil {
		return match[1]
	}
	return ""
}

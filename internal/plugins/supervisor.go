package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// runner supervises one managed plugin's process: it starts it, restarts it with backoff when it
// exits, and gives up after repeated quick crashes.
type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
}

const (
	stopGrace    = 5 * time.Second
	quickCrash   = 15 * time.Second // an exit sooner than this counts towards giving up
	maxQuickExit = 5
)

func (m *Manager) startPlugin(parent context.Context, p *plugin) {
	m.mu.Lock()
	if p.run != nil || p.manifest == nil || p.rec.Attached {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	r := &runner{cancel: cancel, done: make(chan struct{})}
	p.run = r
	p.state, p.detail = "starting", ""
	m.mu.Unlock()
	go func() {
		defer close(r.done)
		m.supervise(ctx, p, r)
	}()
	m.notify(p.id)
}

// stopPlugin asks the plugin to stop, then terminates its process, and waits. For an attached
// plugin it closes the session.
// p.run stays set until the process has exited, so a reconcile meanwhile doesn't start a second
// copy.
func (m *Manager) stopPlugin(p *plugin, reason string) {
	m.mu.Lock()
	r, sess := p.run, p.sess
	m.mu.Unlock()
	if sess != nil {
		sess.stop(reason)
	}
	if r != nil {
		r.cancel()
		<-r.done
	} else if sess != nil {
		select {
		case <-sess.done:
		case <-time.After(stopGrace):
			sess.close(reason)
		}
	}
	m.mu.Lock()
	if r != nil && p.run == r {
		p.run = nil
	}
	if p.run == nil {
		if st, d := p.blocker(); st != "" {
			p.state, p.detail = st, d
		} else if p.state != "crashed" {
			p.state, p.detail = "stopped", reason
		}
	}
	m.mu.Unlock()
	m.notify(p.id)
}

func (m *Manager) supervise(ctx context.Context, p *plugin, r *runner) {
	quick := 0
	for {
		started := time.Now()
		err := m.runOnce(ctx, p)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) < quickCrash {
			quick++
		} else {
			quick = 0
		}
		msg := "the plugin exited"
		if err != nil {
			msg = fmt.Sprintf("the plugin exited: %v", err)
		}
		m.mu.Lock()
		p.restarts++
		if quick >= maxQuickExit {
			p.state, p.detail = "crashed", fmt.Sprintf("%s; it stopped %d times within %s of starting, so it was left off", msg, quick, quickCrash)
			if p.run == r {
				p.run = nil
			}
			m.mu.Unlock()
			p.logs.add("error", "host", p.detail)
			m.log.Error("plugin keeps crashing; giving up", "plugin", p.id, "err", err)
			m.notify(p.id)
			return
		}
		wait := min(time.Minute, time.Second<<quick)
		p.state, p.detail = "restarting", fmt.Sprintf("%s; restarting in %s", msg, wait)
		m.mu.Unlock()
		p.logs.add("warn", "host", p.detail)
		m.log.Warn("plugin exited", "plugin", p.id, "err", err, "restart_in", wait)
		m.notify(p.id)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// runOnce starts the process and waits for it to exit or for ctx to end.
func (m *Manager) runOnce(ctx context.Context, p *plugin) error {
	m.mu.Lock()
	man, dir := p.manifest, p.dir
	token := "rtm_" + randomHex(24)
	p.token = token
	m.mu.Unlock()
	exe, err := man.ExecPath()
	if err != nil {
		return err
	}
	data := filepath.Join(m.dataRoot(), p.id)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(dir, filepath.FromSlash(exe)), man.Run.Managed.Args...)
	cmd.Dir = dir
	cmd.Env = pluginEnv(map[string]string{
		"RT_PLUGIN_ID":     p.id,
		"RT_PLUGIN_SOCKET": m.socketPath(),
		"RT_PLUGIN_TOKEN":  token,
		"RT_PLUGIN_DATA":   data,
		"HOME":             data,
	})
	out := &lineWriter{ring: p.logs, source: "stdout"}
	errw := &lineWriter{ring: p.logs, source: "stderr"}
	cmd.Stdout, cmd.Stderr = out, errw
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)
	// Before Start: a plugin that connects at once must not have its "running" overwritten.
	m.mu.Lock()
	p.startedAt = time.Now()
	p.state, p.detail = "starting", "Waiting for the plugin to connect"
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if p.token == token {
			p.token = "" // a stopped process's token stops working
		}
		m.mu.Unlock()
	}()
	if err := cmd.Start(); err != nil {
		return err
	}
	p.logs.add("info", "host", fmt.Sprintf("started %s (pid %d)", exe, cmd.Process.Pid))
	m.notify(p.id)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		m.dropSession(p, token)
		return err
	case <-ctx.Done():
	}
	// Stopping: the session was already sent Stop; give the process time to leave.
	select {
	case <-exited:
	case <-time.After(stopGrace):
		signalGroup(cmd, false)
		select {
		case <-exited:
		case <-time.After(stopGrace):
			signalGroup(cmd, true)
			<-exited
		}
	}
	m.dropSession(p, token)
	p.logs.add("info", "host", "stopped")
	return nil
}

// dropSession closes the session a process opened with token, if it is still open.
func (m *Manager) dropSession(p *plugin, token string) {
	m.mu.Lock()
	sess := p.sess
	m.mu.Unlock()
	if sess != nil && sess.token == token {
		sess.close("process exited")
	}
}

// pluginEnv is a small environment: the plugin API variables plus what programs need to run.
func pluginEnv(vars map[string]string) []string {
	env := []string{}
	for _, k := range []string{"PATH", "TZ", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	return env
}

// bucket is a send budget: n per hour, up to a burst of a sixth of that (at least 1).
type bucket struct {
	mu      sync.Mutex
	perHour int
	tokens  float64
	last    time.Time
}

func newBucket(perHour int) *bucket {
	return &bucket{perHour: perHour, tokens: float64(burst(perHour)), last: time.Now()}
}

func burst(perHour int) int { return max(1, perHour/6) }

// setPerHour changes the budget, keeping what's been spent.
func (b *bucket) setPerHour(perHour int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.perHour = perHour
	b.tokens = min(b.tokens, float64(burst(perHour)))
}

func (b *bucket) rate() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.perHour
}

// refund gives back a send that didn't happen.
func (b *bucket) refund() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens = min(float64(burst(b.perHour)), b.tokens+1)
}

// take spends one send, or says how long until one is available.
func (b *bucket) take() (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.perHour <= 0 {
		return false, 0
	}
	now := time.Now()
	rate := float64(b.perHour) / 3600
	b.tokens = min(float64(burst(b.perHour)), b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / rate * float64(time.Second))
}

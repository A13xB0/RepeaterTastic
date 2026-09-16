package nodes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
)

// MinFirmware is the oldest meshtasticd a hosted node may run: 2.7 can't take a PKI DM back in
// through the sim envelope (docs/meshtasticd-nodes.md).
const MinFirmware = "2.8.0"

// Instance is one hosted meshtasticd.
type Instance struct {
	Name string // stable and short: names the container and the log lines
	Dir  string // config.yaml and the node's filesystem (vfs) live here
	Port int    // client API port
	HWID string // 12 hex digits: the node's MAC address
}

// ConfigPath is the instance's meshtasticd config file.
func (in Instance) ConfigPath() string { return filepath.Join(in.Dir, "config.yaml") }

// Args are meshtasticd's arguments with the given paths for the config file and filesystem.
func (in Instance) Args(config, vfs string) []string {
	return []string{"-c", config, "-d", vfs, "-h", in.HWID, "-p", strconv.Itoa(in.Port)}
}

// HWIDFor derives a stable, locally administered MAC address from a name.
func HWIDFor(name string) string {
	return fmt.Sprintf("02%010X", uint64(crc32.ChecksumIEEE([]byte(name)))|uint64(0xA5)<<32)
}

// Launcher runs meshtasticd.
type Launcher interface {
	// Run runs the instance until it exits or ctx ends.
	Run(ctx context.Context, in Instance, out io.Writer) error
	// Version reports the meshtasticd version the launcher runs.
	Version(ctx context.Context) (string, error)
	// Describe names the launcher for logs and the GUI.
	Describe() string
}

// ExecLauncher runs a meshtasticd binary directly. Its client API listens on every interface:
// meshtasticd has no bind option, so firewall the port range on a shared network.
type ExecLauncher struct {
	Binary string // path or name on PATH; "" = meshtasticd
}

func (l ExecLauncher) bin() string {
	if l.Binary == "" {
		return "meshtasticd"
	}
	return l.Binary
}

func (l ExecLauncher) Describe() string { return l.bin() }

func (l ExecLauncher) Run(ctx context.Context, in Instance, out io.Writer) error {
	cmd := exec.CommandContext(ctx, l.bin(), in.Args(in.ConfigPath(), filepath.Join(in.Dir, "vfs"))...)
	cmd.Dir = in.Dir
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

func (l ExecLauncher) Version(ctx context.Context) (string, error) {
	return versionFrom(exec.CommandContext(ctx, l.bin(), "--version"))
}

// DockerLauncher runs meshtasticd in a container from a meshtasticd image, its API published on
// 127.0.0.1 only. For hosts without the meshtasticd package.
type DockerLauncher struct {
	Image string
}

func (l DockerLauncher) Describe() string { return "docker " + l.Image }

func (l DockerLauncher) container(in Instance) string {
	return "repeatertastic-" + in.Name + "-" + strconv.Itoa(in.Port)
}

func (l DockerLauncher) Run(ctx context.Context, in Instance, out io.Writer) error {
	name := l.container(in)
	_ = exec.Command("docker", "rm", "-f", name).Run() // a container left by a crash
	args := []string{"run", "--rm", "--name", name, "--label", "repeatertastic.hosted=1",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", in.Port, in.Port),
		"-v", in.Dir + ":/data", l.Image, "/usr/bin/meshtasticd"}
	args = append(args, in.Args("/data/config.yaml", "/data/vfs")...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Cancel = func() error { return exec.Command("docker", "stop", "-t", "3", name).Run() }
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

func (l DockerLauncher) Version(ctx context.Context) (string, error) {
	return versionFrom(exec.CommandContext(ctx, "docker", "run", "--rm", l.Image, "/usr/bin/meshtasticd", "--version"))
}

var versionRE = regexp.MustCompile(`\b(\d+\.\d+\.\d+)(\.[0-9a-f]+)?`)

func versionFrom(cmd *exec.Cmd) (string, error) {
	b, err := cmd.CombinedOutput()
	if m := versionRE.FindString(string(b)); m != "" {
		return m, nil
	}
	if err != nil {
		return "", fmt.Errorf("meshtasticd --version: %w", err)
	}
	return "", errors.New("meshtasticd --version printed no version")
}

// VersionAtLeast compares dotted versions by their first three numbers.
func VersionAtLeast(v, min string) bool {
	parse := func(s string) [3]int {
		var out [3]int
		for i, p := range strings.SplitN(s, ".", 4) {
			if i > 2 {
				break
			}
			out[i], _ = strconv.Atoi(p)
		}
		return out
	}
	a, b := parse(v), parse(min)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

// instanceConfig is a hosted node's meshtasticd config: a sim radio and nothing else. UDP
// multicast and MQTT stay off (the air bridge and RepeaterTastic's own links carry those).
const instanceConfig = `# written by RepeaterTastic: a hosted node on a simulated radio
Lora:
  Module: sim
Logging:
  LogLevel: info
General:
  MaxNodes: 200
  MaxMessageQueue: 100
`

// Hosted is a meshtasticd RepeaterTastic runs, standing in for one of a host's identities.
type Hosted struct {
	*Node
	inst     Instance
	launcher Launcher

	mu       sync.Mutex
	restarts int
	lastErr  string
	running  bool
	logTail  []string
}

// StartHosted writes the instance's config, starts meshtasticd under supervision and connects
// its client. The process restarts (with backoff) until ctx ends.
func StartHosted(ctx context.Context, l Launcher, in Instance, logf func(string, ...any)) (*Hosted, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := os.MkdirAll(filepath.Join(in.Dir, "vfs"), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(in.ConfigPath(), []byte(instanceConfig), 0o600); err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", in.Port)
	c := mtclient.New(mtclient.Options{Address: addr, Logf: func(string, ...any) {}, ReconnectInterval: 500 * time.Millisecond,
		ConfigTimeout: 30 * time.Second})
	h := &Hosted{Node: newNode(addr, in.Dir, c, logf, false), inst: in, launcher: l}
	go h.supervise(ctx)
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

// Instance describes the hosted node.
func (h *Hosted) Instance() Instance { return h.inst }

// HostedStatus is what the GUI shows about a hosted node.
type HostedStatus struct {
	Name      string   `json:"name"`
	Launcher  string   `json:"launcher"`
	Port      int      `json:"port"`
	Running   bool     `json:"running"`
	Connected bool     `json:"connected"`
	Restarts  int      `json:"restarts"`
	LastError string   `json:"last_error,omitempty"`
	Firmware  string   `json:"firmware,omitempty"`
	NodeID    string   `json:"node_id,omitempty"`
	Log       []string `json:"log,omitempty"`
}

// Status reports the process and link state.
func (h *Hosted) Status() HostedStatus {
	s := h.client.Snapshot()
	h.mu.Lock()
	defer h.mu.Unlock()
	st := HostedStatus{Name: h.inst.Name, Launcher: h.launcher.Describe(), Port: h.inst.Port, Running: h.running,
		Connected: s.Connected, Restarts: h.restarts, LastError: h.lastErr, Firmware: s.Metadata.GetFirmwareVersion(),
		Log: append([]string(nil), h.logTail...)}
	if s.NodeNum() != 0 {
		st.NodeID = fmt.Sprintf("!%08x", s.NodeNum())
	}
	return st
}

func (h *Hosted) supervise(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		pr, pw := io.Pipe()
		go h.collect(pr)
		h.mu.Lock()
		h.running = true
		h.mu.Unlock()
		start := time.Now()
		err := h.launcher.Run(ctx, h.inst, pw)
		pw.Close()
		h.mu.Lock()
		h.running = false
		if ctx.Err() == nil {
			h.restarts++
			if err == nil {
				err = errors.New("exited")
			}
			h.lastErr = err.Error()
		}
		h.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		h.logf("meshtasticd %s (%s) stopped: %v; restarting in %v", h.inst.Name, h.launcher.Describe(), err, backoff)
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// collect keeps the last lines meshtasticd printed, for the GUI and for errors.
func (h *Hosted) collect(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		h.mu.Lock()
		h.logTail = append(h.logTail, line)
		if len(h.logTail) > 200 {
			h.logTail = h.logTail[len(h.logTail)-200:]
		}
		h.mu.Unlock()
		if (strings.Contains(line, "ERROR") || strings.Contains(line, "CRIT")) && !bootNoise(line) {
			h.logf("meshtasticd %s: %s", h.inst.Name, line)
		}
	}
}

// Close stops the client; the process stops with the context StartHosted was given.
func (h *Hosted) Close() error { return h.client.Close() }

var _ mesh.ConfigApplier = (*Hosted)(nil)

// bootNoise is an error line every fresh or radio-less meshtasticd prints.
func bootNoise(line string) bool {
	return strings.Contains(line, "Can't open/read /prefs/") || strings.Contains(line, "No radio instance available to provide entropy")
}

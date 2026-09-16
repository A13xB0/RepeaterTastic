package nodes

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultImage is the meshtasticd image offered when none is set.
const DefaultImage = "meshtastic/meshtasticd:2.8.0.47db0e3-alpha-debian"

// InstalledRuntime is meshtasticd installed on this machine.
type InstalledRuntime struct {
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	OK      bool   `json:"ok"` // new enough to host nodes
	Error   string `json:"error,omitempty"`
}

// DockerRuntime is Docker on this machine.
type DockerRuntime struct {
	Found bool   `json:"found"`
	OK    bool   `json:"ok"` // the daemon answers and this user may use it
	Error string `json:"error,omitempty"`
	// Version is the Docker server's version.
	Version string `json:"version,omitempty"`
	// Image is the meshtasticd image checked, and ImagePresent whether it is already downloaded.
	Image        string `json:"image"`
	ImagePresent bool   `json:"image_present"`
}

// Runtimes is what this machine can run hosted nodes with.
type Runtimes struct {
	Meshtasticd InstalledRuntime `json:"meshtasticd"`
	Docker      DockerRuntime    `json:"docker"`
	MinVersion  string           `json:"min_version"`
}

// DetectRuntimes looks for an installed meshtasticd (binary "" = on PATH) and for Docker with
// image ("" = DefaultImage). It runs nothing that downloads or starts a node.
func DetectRuntimes(ctx context.Context, binary, image string) Runtimes {
	if image == "" {
		image = DefaultImage
	}
	out := Runtimes{MinVersion: MinFirmware, Docker: DockerRuntime{Image: image}}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		out.Meshtasticd = detectInstalled(ctx, binary)
	}()
	go func() {
		defer wg.Done()
		detectDocker(ctx, &out.Docker)
	}()
	wg.Wait()
	return out
}

func detectInstalled(ctx context.Context, binary string) InstalledRuntime {
	if binary == "" {
		binary = "meshtasticd"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return InstalledRuntime{Error: "meshtasticd isn't installed"}
	}
	r := InstalledRuntime{Found: true, Path: path}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := CheckLauncher(ctx, ExecLauncher{Binary: path})
	r.Version, r.OK = v, err == nil
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

func detectDocker(ctx context.Context, d *DockerRuntime) {
	if _, err := exec.LookPath("docker"); err != nil {
		d.Error = "Docker isn't installed"
		return
	}
	d.Found = true
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(b))
		if strings.Contains(msg, "permission denied") {
			msg = "this user can't use Docker: add it to the docker group"
		} else if msg == "" {
			msg = err.Error()
		}
		d.Error = firstLine(msg)
		return
	}
	d.OK, d.Version = true, strings.TrimSpace(string(b))
	d.ImagePresent = exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", d.Image).Run() == nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

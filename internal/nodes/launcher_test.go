package nodes

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fake programs for the launchers. They record their arguments in $FAKE_DIR/<name>.args and act
// on a few environment variables, so tests can run ExecLauncher and DockerLauncher without
// meshtasticd or Docker.
const fakeMeshtasticd = `#!/bin/sh
if [ "$1" = "--version" ]; then
  [ -n "$FAKE_VERSION" ] && echo "$FAKE_VERSION"
  exit "${FAKE_VERSION_RC:-0}"
fi
echo "$@" > "$FAKE_DIR/meshtasticd.args"
pwd > "$FAKE_DIR/meshtasticd.cwd"
echo "INFO  | booting"
if [ -n "$FAKE_STAY" ]; then
  trap 'echo stopping; exit 0' INT
  while :; do sleep 0.02; done
fi
exit "${FAKE_RUN_RC:-0}"
`

const fakeDocker = `#!/bin/sh
echo "$@" >> "$FAKE_DIR/docker.args"
case "$1" in
version)
  [ -n "$FAKE_DOCKER_OUT" ] && echo "$FAKE_DOCKER_OUT"
  exit "${FAKE_DOCKER_RC:-0}" ;;
image)
  exit "${FAKE_IMAGE_RC:-0}" ;;
rm)
  exit 0 ;;
stop)
  kill "$(cat "$FAKE_DIR/run.pid")"
  exit 0 ;;
run)
  case "$*" in
  *--version*) echo "meshtasticd 2.8.0.47db0e3"; exit 0 ;;
  esac
  echo $$ > "$FAKE_DIR/run.pid"
  echo "container up"
  if [ -n "$FAKE_STAY" ]; then
    trap 'exit 0' TERM
    while :; do sleep 0.02; done
  fi
  exit 0 ;;
esac
exit 3
`

// fakeBin puts the fake programs first on PATH and returns their directory.
func fakeBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, script := range map[string]string{"meshtasticd": fakeMeshtasticd, "docker": fakeDocker} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// syncBuffer is a bytes.Buffer safe for the command's output copier and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func testInstance(t *testing.T) Instance {
	return Instance{Name: "main-abc", Dir: t.TempDir(), Port: 4501, HWID: "02A5DEADBEEF"}
}

func TestInstanceArgs(t *testing.T) {
	in := testInstance(t)
	got := strings.Join(in.Args("/c.yaml", "/vfs"), " ")
	if got != "-c /c.yaml -d /vfs -h 02A5DEADBEEF -p 4501" {
		t.Fatalf("args %q", got)
	}
	if in.ConfigPath() != filepath.Join(in.Dir, "config.yaml") {
		t.Fatalf("config path %q", in.ConfigPath())
	}
}

func TestExecLauncherRun(t *testing.T) {
	dir := fakeBin(t)
	in := testInstance(t)
	var out syncBuffer
	l := ExecLauncher{}
	if l.Describe() != "meshtasticd" || (ExecLauncher{Binary: "/opt/x/meshtasticd"}).Describe() != "/opt/x/meshtasticd" {
		t.Fatal("describe")
	}
	if err := l.Run(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	want := "-c " + in.ConfigPath() + " -d " + filepath.Join(in.Dir, "vfs") + " -h 02A5DEADBEEF -p 4501"
	if got := readFile(t, filepath.Join(dir, "meshtasticd.args")); got != want {
		t.Fatalf("args %q, want %q", got, want)
	}
	if cwd, _ := filepath.EvalSymlinks(readFile(t, filepath.Join(dir, "meshtasticd.cwd"))); cwd != mustEval(t, in.Dir) {
		t.Fatalf("ran in %q", cwd)
	}
	if !strings.Contains(out.String(), "booting") {
		t.Fatalf("output %q", out.String())
	}
	t.Setenv("FAKE_RUN_RC", "2")
	if err := l.Run(context.Background(), in, &out); err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Fatalf("failing run: %v", err)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A cancelled run interrupts meshtasticd so it can shut down cleanly.
func TestExecLauncherStopsOnCancel(t *testing.T) {
	fakeBin(t)
	t.Setenv("FAKE_STAY", "1")
	ctx, cancel := context.WithCancel(context.Background())
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- ExecLauncher{}.Run(ctx, testInstance(t), &out) }()
	eventually(t, "meshtasticd started", func() bool { return strings.Contains(out.String(), "booting") })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	eventually(t, "clean shutdown", func() bool { return strings.Contains(out.String(), "stopping") })
}

func TestCheckLauncherVersions(t *testing.T) {
	fakeBin(t)
	cases := []struct {
		name, out, rc, want, wantErr string
	}{
		{"new enough", "meshtasticd 2.8.1.abc123 (sim)", "0", "2.8.1.abc123", ""},
		{"too old", "2.7.15.567b8ea", "0", "2.7.15.567b8ea", "too old"},
		{"version with a failing exit", "2.8.0", "1", "2.8.0", ""},
		{"fails silently", "", "1", "", "can't run"},
		{"no version printed", "hello", "0", "", "printed no version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_VERSION", tc.out)
			t.Setenv("FAKE_VERSION_RC", tc.rc)
			v, err := CheckLauncher(context.Background(), ExecLauncher{})
			if v != tc.want || !errorMentions(err, tc.wantErr) {
				t.Fatalf("got %q, %v", v, err)
			}
		})
	}
}

func errorMentions(err error, want string) bool {
	if want == "" {
		return err == nil
	}
	return err != nil && strings.Contains(err.Error(), want)
}

func TestCheckLauncherNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := CheckLauncher(context.Background(), LauncherFor("", ""))
	if err == nil || err.Error() != "there's no meshtasticd at meshtasticd" {
		t.Fatalf("err = %v", err)
	}
}

func TestLauncherFor(t *testing.T) {
	if l, ok := LauncherFor("/usr/bin/meshtasticd", "").(ExecLauncher); !ok || l.Binary != "/usr/bin/meshtasticd" {
		t.Fatalf("exec launcher %#v", l)
	}
	l := LauncherFor("/usr/bin/meshtasticd", "meshtastic/meshtasticd:2.8")
	if d, ok := l.(DockerLauncher); !ok || d.Image != "meshtastic/meshtasticd:2.8" || l.Describe() != "docker meshtastic/meshtasticd:2.8" {
		t.Fatalf("docker launcher %#v", l)
	}
}

func TestImageAndBinaryNames(t *testing.T) {
	images := map[string]bool{
		"meshtastic/meshtasticd:2.8.0":           true,
		"docker.io/meshtastic/meshtasticd:beta":  true,
		"evil/meshtasticd:2.8.0":                 false,
		"meshtastic/meshtasticd":                 false,
		"ghcr.io/meshtastic/meshtasticd:2.8.0.1": false,
	}
	for img, want := range images {
		if OfficialImage(img) != want {
			t.Errorf("OfficialImage(%q) = %v", img, !want)
		}
	}
	bins := map[string]bool{"": true, "/usr/local/bin/meshtasticd": true, "meshtasticd": true, "/bin/sh": false}
	for b, want := range bins {
		if MeshtasticdBinary(b) != want {
			t.Errorf("MeshtasticdBinary(%q) = %v", b, !want)
		}
	}
}

func TestDockerLauncherRun(t *testing.T) {
	dir := fakeBin(t)
	in := testInstance(t)
	l := DockerLauncher{Image: "meshtastic/meshtasticd:test"}
	var out syncBuffer
	if err := l.Run(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(readFile(t, filepath.Join(dir, "docker.args")), "\n")
	if len(lines) != 2 || lines[0] != "rm -f repeatertastic-main-abc-4501" {
		t.Fatalf("docker calls %q", lines)
	}
	run := lines[1]
	for _, want := range []string{"run --rm --name repeatertastic-main-abc-4501 ", "-p 127.0.0.1:4501:4501 ",
		"-v " + in.Dir + ":/data meshtastic/meshtasticd:test /usr/bin/meshtasticd -c /data/config.yaml -d /data/vfs -h 02A5DEADBEEF -p 4501"} {
		if !strings.Contains(run, want) {
			t.Errorf("run %q lacks %q", run, want)
		}
	}
	if !strings.Contains(out.String(), "container up") {
		t.Fatalf("output %q", out.String())
	}
	v, err := l.Version(context.Background())
	if err != nil || v != "2.8.0.47db0e3" {
		t.Fatalf("version %q, %v", v, err)
	}
}

// Cancelling a container run stops the container rather than killing the docker client.
func TestDockerLauncherStopsOnCancel(t *testing.T) {
	dir := fakeBin(t)
	t.Setenv("FAKE_STAY", "1")
	ctx, cancel := context.WithCancel(context.Background())
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- DockerLauncher{Image: "img"}.Run(ctx, testInstance(t), &out) }()
	eventually(t, "container started", func() bool { return strings.Contains(out.String(), "container up") })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "docker.args")), "stop -t 3 repeatertastic-main-abc-4501") {
		t.Fatal("container not stopped")
	}
}

func TestDetectRuntimesFound(t *testing.T) {
	dir := fakeBin(t)
	t.Setenv("FAKE_VERSION", "2.8.1.aaa")
	t.Setenv("FAKE_DOCKER_OUT", "27.3.1")
	r := DetectRuntimes(context.Background(), "", "")
	m, d := r.Meshtasticd, r.Docker
	if !m.Found || !m.OK || m.Version != "2.8.1.aaa" || m.Path != filepath.Join(dir, "meshtasticd") || m.Error != "" {
		t.Fatalf("meshtasticd %+v", m)
	}
	if !d.Found || !d.OK || d.Version != "27.3.1" || d.Image != DefaultImage || !d.ImagePresent {
		t.Fatalf("docker %+v", d)
	}
	if r.MinVersion != MinFirmware {
		t.Fatalf("min version %q", r.MinVersion)
	}
}

func TestDetectRuntimesProblems(t *testing.T) {
	fakeBin(t)
	t.Setenv("FAKE_VERSION", "2.6.0")
	t.Setenv("FAKE_IMAGE_RC", "1")
	cases := []struct{ out, want string }{
		{"Got permission denied while trying to connect\nmore", "this user can't use Docker: add it to the docker group"},
		{"Cannot connect to the Docker daemon\nIs it running?", "Cannot connect to the Docker daemon"},
		{"", "exit status 1"},
	}
	for _, tc := range cases {
		t.Setenv("FAKE_DOCKER_OUT", tc.out)
		t.Setenv("FAKE_DOCKER_RC", "1")
		r := DetectRuntimes(context.Background(), "meshtasticd", "custom:img")
		if d := r.Docker; !d.Found || d.OK || d.Error != tc.want || d.Image != "custom:img" {
			t.Errorf("%q: docker %+v", tc.out, d)
		}
		if m := r.Meshtasticd; !m.Found || m.OK || !strings.Contains(m.Error, "too old") {
			t.Errorf("meshtasticd %+v", m)
		}
	}
	t.Setenv("FAKE_DOCKER_RC", "0")
	if d := DetectRuntimes(context.Background(), "", "x").Docker; !d.OK || d.ImagePresent {
		t.Fatalf("missing image: %+v", d)
	}
}

func TestDetectRuntimesNothingInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := DetectRuntimes(context.Background(), "", "")
	if r.Meshtasticd.Found || r.Meshtasticd.Error != "meshtasticd isn't installed" {
		t.Fatalf("meshtasticd %+v", r.Meshtasticd)
	}
	if r.Docker.Found || r.Docker.Error != "Docker isn't installed" {
		t.Fatalf("docker %+v", r.Docker)
	}
}

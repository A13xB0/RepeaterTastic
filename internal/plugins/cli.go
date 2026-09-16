package plugins

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/config"
)

const cliUsage = `usage: repeatertastic plugin [-config file] <command>

  list                          installed plugins and their state
  install <bundle.zip | URL>    install or upgrade a plugin (it stays off until enabled)
  enable <id> [permission ...]  turn a plugin on; "all" grants every permission it asks for
  disable <id>                  turn a plugin off
  remove <id> [-keep-data]      delete a plugin (and its data folder unless -keep-data)
  permissions                   the permissions plugins can ask for

Run it as the user RepeaterTastic runs as (sudo -u repeatertastic ...). A running daemon picks
changes up within a few seconds.
`

// CLI runs "repeatertastic plugin ...".
func CLI(cfg *config.Config, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" {
		fmt.Fprint(stdout, cliUsage)
		return nil
	}
	dir := cfg.PluginDir()
	m, err := New(Options{Config: cfg.Plugins, Dir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return err
	}
	defer chownLike(dir)
	switch args[0] {
	case "list", "ls":
		return cliList(m, stdout)
	case "permissions":
		for _, k := range sortedKeys(Permissions) {
			fmt.Fprintf(stdout, "%-16s %s\n", k, Permissions[k])
		}
		return nil
	}
	run, ok := cliCommands[args[0]]
	if !ok {
		return fmt.Errorf("unknown command %q\n\n%s", args[0], cliUsage)
	}
	if len(args) < 2 {
		return fmt.Errorf("%s needs %d argument(s)\n\n%s", args[0], 1, cliUsage)
	}
	return run(m, args[1], args[2:], stdout)
}

// cliCommands are the commands that act on one plugin (or bundle): the argument, then the rest.
var cliCommands = map[string]func(m *Manager, arg string, rest []string, stdout io.Writer) error{
	"install": func(m *Manager, src string, _ []string, stdout io.Writer) error {
		return cliInstall(m, src, stdout)
	},
	"enable":  cliEnable,
	"disable": cliDisable,
	"remove":  cliRemove,
	"rm":      cliRemove,
}

// cliList prints the installed plugins as a table.
func cliList(m *Manager, stdout io.Writer) error {
	tw := tabwriter.NewWriter(stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tVERSION\tKIND\tSWITCH\tGRANTED")
	for _, in := range m.List() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", in.ID, in.Name, in.Version, in.Kind, cliSwitch(in), strings.Join(grantedKeys(in), ","))
	}
	return tw.Flush()
}

// cliSwitch describes a plugin's switch. This process isn't the daemon, so it can only say whether
// a plugin is on and whether something stops it running.
func cliSwitch(in Info) string {
	state := "off"
	if in.Enabled {
		state = "on"
	}
	switch in.State {
	case "needs_review", "needs_settings", "unsupported", "waiting":
		state += ", " + strings.ReplaceAll(in.State, "_", " ")
	}
	if in.Pinned {
		state += " (config file)"
	}
	return state
}

// grantedKeys lists the permissions the plugin has been granted.
func grantedKeys(in Info) []string {
	var granted []string
	for _, p := range in.Permissions {
		if p.Granted {
			granted = append(granted, p.Key)
		}
	}
	return granted
}

// cliEnable turns a plugin on with the listed permissions ("all" for every one it asks for).
func cliEnable(m *Manager, id string, granted []string, stdout io.Writer) error {
	in, err := m.Get(id)
	if err != nil {
		return err
	}
	if len(granted) == 1 && granted[0] == "all" {
		granted = nil
		for _, p := range in.Permissions {
			granted = append(granted, p.Key)
		}
	}
	if err := m.Enable(id, granted); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s enabled with %s\n", in.Name, permList(granted))
	return nil
}

// cliDisable turns a plugin off.
func cliDisable(m *Manager, id string, _ []string, stdout io.Writer) error {
	if err := m.Disable(id); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s disabled\n", id)
	return nil
}

// cliRemove deletes a plugin, and its data folder unless -keep-data is given.
func cliRemove(m *Manager, id string, rest []string, stdout io.Writer) error {
	fsx := flag.NewFlagSet("remove", flag.ContinueOnError)
	keep := fsx.Bool("keep-data", false, "keep the plugin's data folder")
	if err := fsx.Parse(rest); err != nil {
		return err
	}
	if err := m.Remove(id, *keep); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s removed\n", id)
	return nil
}

// cliInstall installs directly, or hands the bundle to a running daemon through the inbox so it
// can stop the old version first.
func cliInstall(m *Manager, src string, stdout io.Writer) error {
	f, done, err := openBundle(m, src)
	if err != nil {
		return err
	}
	defer done()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if daemonRunning(m.socketPath()) {
		return handToDaemon(m, src, io.NewSectionReader(f, 0, st.Size()), stdout)
	}
	man, err := m.Install(f, st.Size(), "cli")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Installed %s %s (%s). It is off: repeatertastic plugin enable %s all\n", man.Name, man.Version, man.ID, man.ID)
	return nil
}

// openBundle opens a bundle file, or downloads one from a URL; done closes it and removes any
// download.
func openBundle(m *Manager, src string) (f *os.File, done func(), err error) {
	if !isHTTPURL(src) {
		if f, err = os.Open(src); err != nil {
			return nil, nil, err
		}
		return f, func() { f.Close() }, nil
	}
	if f, err = Download(context.Background(), src, filepath.Join(m.inboxDir(), ".tmp")); err != nil {
		return nil, nil, err
	}
	return f, func() {
		f.Close()
		os.Remove(f.Name())
	}, nil
}

// handToDaemon drops the bundle into the inbox for the running daemon. It's written under a
// temporary name first so the daemon never picks up half a file.
func handToDaemon(m *Manager, src string, r io.Reader, stdout io.Writer) error {
	dst := filepath.Join(m.inboxDir(), bundleName(src))
	if !strings.HasSuffix(strings.ToLower(dst), ".zip") {
		dst += ".zip"
	}
	if err := copyToFile(dst+".part", r); err != nil {
		return err
	}
	if err := os.Rename(dst+".part", dst); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Handed %s to the running RepeaterTastic; it installs within a few seconds.\n", bundleName(src))
	fmt.Fprintf(stdout, "Rejected bundles go to %s with the reason.\n", filepath.Join(m.inboxDir(), ".rejected"))
	return nil
}

// copyToFile writes r to a new file at name.
func copyToFile(name string, r io.Reader) error {
	out, err := os.Create(name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func daemonRunning(sock string) bool {
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// chownLike gives files root created under dir to dir's owner, so the daemon can use them. It
// works through an os.Root, so a link swapped in by a plugin can't point the chown outside dir.
func chownLike(dir string) {
	if os.Geteuid() != 0 {
		return
	}
	st, err := os.Stat(dir)
	if err != nil {
		return
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid == 0 {
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return
	}
	defer root.Close()
	_ = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			if s, ok := info.Sys().(*syscall.Stat_t); ok && s.Uid == 0 {
				_ = root.Lchown(p, int(sys.Uid), int(sys.Gid))
			}
		}
		return nil
	})
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

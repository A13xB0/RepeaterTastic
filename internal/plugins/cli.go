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

	"github.com/A13xB0/RepeaterTastic/internal/config"
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
	need := func(n int) error {
		if len(args) < n+1 {
			return fmt.Errorf("%s needs %d argument(s)\n\n%s", args[0], n, cliUsage)
		}
		return nil
	}
	switch args[0] {
	case "list", "ls":
		tw := tabwriter.NewWriter(stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tVERSION\tKIND\tSTATE\tPERMISSIONS")
		for _, in := range m.List() {
			var granted []string
			for _, p := range in.Permissions {
				if p.Granted {
					granted = append(granted, p.Key)
				}
			}
			state := in.State
			if in.Pinned {
				state += " (config file)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", in.ID, in.Name, in.Version, in.Kind, state, strings.Join(granted, ","))
		}
		return tw.Flush()
	case "permissions":
		for _, k := range sortedKeys(Permissions) {
			fmt.Fprintf(stdout, "%-16s %s\n", k, Permissions[k])
		}
		return nil
	case "install":
		if err := need(1); err != nil {
			return err
		}
		return cliInstall(m, args[1], stdout)
	case "enable":
		if err := need(1); err != nil {
			return err
		}
		in, err := m.Get(args[1])
		if err != nil {
			return err
		}
		granted := args[2:]
		if len(granted) == 1 && granted[0] == "all" {
			granted = nil
			for _, p := range in.Permissions {
				granted = append(granted, p.Key)
			}
		}
		if err := m.Enable(args[1], granted); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s enabled with %s\n", in.Name, permList(granted))
		return nil
	case "disable":
		if err := need(1); err != nil {
			return err
		}
		if err := m.Disable(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s disabled\n", args[1])
		return nil
	case "remove", "rm":
		fsx := flag.NewFlagSet("remove", flag.ContinueOnError)
		keep := fsx.Bool("keep-data", false, "keep the plugin's data folder")
		if err := need(1); err != nil {
			return err
		}
		id := args[1]
		if err := fsx.Parse(args[2:]); err != nil {
			return err
		}
		if err := m.Remove(id, *keep); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s removed\n", id)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", args[0], cliUsage)
}

// cliInstall installs directly, or hands the bundle to a running daemon through the inbox so it
// can stop the old version first.
func cliInstall(m *Manager, src string, stdout io.Writer) error {
	var f *os.File
	var err error
	if strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") {
		if f, err = Download(context.Background(), src, filepath.Join(m.inboxDir(), ".tmp")); err != nil {
			return err
		}
		defer os.Remove(f.Name())
	} else if f, err = os.Open(src); err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if daemonRunning(m.socketPath()) {
		dst := filepath.Join(m.inboxDir(), bundleName(src))
		if !strings.HasSuffix(strings.ToLower(dst), ".zip") {
			dst += ".zip"
		}
		out, err := os.Create(dst + ".part")
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.NewSectionReader(f, 0, st.Size())); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		if err := os.Rename(dst+".part", dst); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Handed %s to the running RepeaterTastic; it installs within a few seconds.\n", bundleName(src))
		fmt.Fprintf(stdout, "Rejected bundles go to %s with the reason.\n", filepath.Join(m.inboxDir(), ".rejected"))
		return nil
	}
	man, err := m.Install(f, st.Size(), "cli")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Installed %s %s (%s). It is off: repeatertastic plugin enable %s all\n", man.Name, man.Version, man.ID, man.ID)
	return nil
}

func daemonRunning(sock string) bool {
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// chownLike gives files root created under dir to dir's owner, so the daemon can use them.
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
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			if s, ok := info.Sys().(*syscall.Stat_t); ok && s.Uid == 0 {
				_ = os.Lchown(p, int(sys.Uid), int(sys.Gid))
			}
		}
		return nil
	})
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iMarand/magnetk/go/pkg/magnetk"
)

// OpenMain implements "magnetk open <uri>": what the operating system runs
// when a magnetk:// address or magnetk:? file link is clicked (see
// "magnetk install").
//
//	magnetk:?xt=urn:sha256:...                -> download (to ~/Downloads)
//	magnetk://node@relay[/chat][?text=...]    -> chat with the node
//	magnetk://relay:4002                      -> ping the relay
func OpenMain(args []string) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	pause := fs.Bool("pause", false, "wait for Enter before exiting (used by the link handler so the window stays open)")
	out := fs.String("o", "", "download directory for file links (default ~/Downloads)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: magnetk open [-pause] <magnetk://address | magnetk:?file-link>")
	}
	uri := strings.TrimSpace(fs.Arg(0))
	if *pause {
		defer func() {
			fmt.Print("\nPress Enter to close.")
			bufio.NewReader(os.Stdin).ReadString('\n')
		}()
	}
	// Commands run from a link handler start in an arbitrary directory
	// (C:\Windows\System32, /); use the user's settings and folders.
	settings := userSettingsPath()
	switch {
	case magnetk.IsLink(uri):
		dir := *out
		if dir == "" {
			dir = downloadsDir()
		}
		os.MkdirAll(dir, 0o755)
		fmt.Printf("downloading to %s\n", dir)
		err := GetMain([]string{"-config", settings, "-o", dir, uri})
		reportOpen(err)
		return err
	case magnetk.IsAddress(uri):
		a, err := magnetk.ParseAddress(uri)
		if err != nil {
			reportOpen(err)
			return err
		}
		if a.Node == "" {
			err := PingMain([]string{"-config", settings, "-relay", strings.Join(a.Relays, ","), "-n", "5"})
			reportOpen(err)
			return err
		}
		home, _ := os.UserHomeDir()
		err = ChatMain([]string{"-config", settings, "-dir", filepath.Join(home, "Magnetk", "chat"), uri})
		reportOpen(err)
		return err
	}
	err := fmt.Errorf("not a magnetk address or link: %q", uri)
	reportOpen(err)
	return err
}

func reportOpen(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}

// userSettingsPath prefers ./config/settings.conf, then ~/.magnetk/settings.conf.
func userSettingsPath() string {
	if _, err := os.Stat("config/settings.conf"); err == nil {
		return "config/settings.conf"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".magnetk", "settings.conf")
}

func downloadsDir() string {
	home, _ := os.UserHomeDir()
	d := filepath.Join(home, "Downloads")
	if st, err := os.Stat(d); err == nil && st.IsDir() {
		return filepath.Join(d, "Magnetk")
	}
	return filepath.Join(home, "Magnetk", "downloads")
}

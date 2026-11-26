package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// InstallMain implements "magnetk install": registers magnetk:// addresses
// and magnetk:? file links with the desktop so clicking one runs
// "magnetk open <link>" in a terminal window. Per user, no admin rights.
//
//	Windows  HKCU\Software\Classes\magnetk -> "magnetk.exe" open -pause "%1"
//	Linux    ~/.local/share/applications/magnetk-link.desktop + xdg-mime
//	macOS    ~/Applications/Magnetk Links.app (AppleScript applet) + LaunchServices
//
// On a server without a desktop (a VPS relay or seed) it does nothing:
// there is nothing to click there, and "magnetk open <link>" works anyway.
func InstallMain(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	uninstall := fs.Bool("uninstall", false, "remove the link handler")
	force := fs.Bool("force", false, "register even if no desktop session is detected")
	dry := fs.Bool("n", false, "print what would be done")
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	exe, _ = filepath.Abs(exe)
	if !*uninstall && !*force && !hasDesktop() {
		fmt.Println("no desktop session detected (server): skipping the magnetk:// link handler.")
		fmt.Println("Links still work from the command line: magnetk open 'magnetk://node@relay/chat'")
		return nil
	}
	h := linkHandler{exe: exe, dry: *dry}
	switch runtime.GOOS {
	case "windows":
		err = h.windows(*uninstall)
	case "darwin":
		err = h.darwin(*uninstall)
	case "linux", "freebsd", "openbsd", "netbsd":
		err = h.xdg(*uninstall)
	default:
		return fmt.Errorf("link handler registration is not supported on %s", runtime.GOOS)
	}
	if err != nil {
		return err
	}
	if *uninstall {
		fmt.Println("magnetk:// link handler removed")
	} else {
		fmt.Printf("magnetk:// and magnetk:? links now open with:\n  %s open -pause <link>\n", exe)
		fmt.Println("Try it: open magnetk://<node>@<relay>/chat in a browser or Run box.")
	}
	return nil
}

func hasDesktop() bool {
	switch runtime.GOOS {
	case "windows", "darwin":
		return true
	}
	for _, v := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_CURRENT_DESKTOP"} {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

type linkHandler struct {
	exe string
	dry bool
}

func (h linkHandler) run(name string, args ...string) error {
	fmt.Printf("  $ %s %s\n", name, strings.Join(args, " "))
	if h.dry {
		return nil
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (h linkHandler) write(path, content string, mode os.FileMode) error {
	fmt.Printf("  write %s\n", path)
	if h.dry {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), mode)
}

// ----- Windows: per-user URL protocol in the registry -----

func (h linkHandler) windows(uninstall bool) error {
	const key = `HKCU\Software\Classes\magnetk`
	if uninstall {
		return h.run("reg", "delete", key, "/f")
	}
	cmd := fmt.Sprintf(`"%s" open -pause "%%1"`, h.exe)
	steps := [][]string{
		{"add", key, "/ve", "/d", "URL:Magnetk link", "/f"},
		{"add", key, "/v", "URL Protocol", "/d", "", "/f"},
		{"add", key + `\DefaultIcon`, "/ve", "/d", fmt.Sprintf(`"%s",0`, h.exe), "/f"},
		{"add", key + `\shell\open\command`, "/ve", "/d", cmd, "/f"},
	}
	for _, s := range steps {
		if err := h.run("reg", s...); err != nil {
			return err
		}
	}
	return nil
}

// ----- Linux / BSD: freedesktop x-scheme-handler -----

func (h linkHandler) xdg(uninstall bool) error {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	apps := filepath.Join(dataHome, "applications")
	file := filepath.Join(apps, "magnetk-link.desktop")
	if uninstall {
		fmt.Printf("  remove %s\n", file)
		if !h.dry {
			if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		h.refreshXDG(apps)
		return nil
	}
	desktop := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Magnetk
Comment=Open magnetk:// addresses and magnetk: file links
Exec="%s" open -pause %%u
Terminal=true
NoDisplay=true
MimeType=x-scheme-handler/magnetk;
Categories=Network;
`, h.exe)
	if err := h.write(file, desktop, 0o644); err != nil {
		return err
	}
	if _, err := exec.LookPath("xdg-mime"); err == nil {
		if err := h.run("xdg-mime", "default", "magnetk-link.desktop", "x-scheme-handler/magnetk"); err != nil {
			return err
		}
	} else {
		fmt.Println("  (xdg-mime not found: install xdg-utils to make the handler the default)")
	}
	h.refreshXDG(apps)
	return nil
}

func (h linkHandler) refreshXDG(apps string) {
	if _, err := exec.LookPath("update-desktop-database"); err == nil {
		h.run("update-desktop-database", apps)
	}
}

// ----- macOS: AppleScript applet that receives the URL Apple Event -----

func (h linkHandler) darwin(uninstall bool) error {
	home, _ := os.UserHomeDir()
	app := filepath.Join(home, "Applications", "Magnetk Links.app")
	lsregister := "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	if uninstall {
		if _, err := os.Stat(app); err == nil {
			h.run(lsregister, "-u", app)
		}
		fmt.Printf("  remove %s\n", app)
		if h.dry {
			return nil
		}
		return os.RemoveAll(app)
	}
	// macOS hands URLs to apps as Apple Events, not argv: an AppleScript
	// applet with "on open location" receives them and opens Terminal.
	script := fmt.Sprintf(`on open location theURL
	set cmd to quoted form of %q & " open -pause " & quoted form of theURL
	tell application "Terminal"
		activate
		do script cmd
	end tell
end open location

on run
	display dialog "Magnetk Links opens magnetk:// links in Terminal." buttons {"OK"} default button 1
end run
`, h.exe)
	src := filepath.Join(os.TempDir(), "magnetk-links.applescript")
	if err := h.write(src, script, 0o644); err != nil {
		return err
	}
	if !h.dry {
		os.RemoveAll(app)
	}
	if err := h.run("osacompile", "-o", app, src); err != nil {
		return err
	}
	plist := filepath.Join(app, "Contents", "Info.plist")
	if err := h.run("plutil", "-replace", "CFBundleIdentifier", "-string", "com.magnetk.links", plist); err != nil {
		return err
	}
	if err := h.run("plutil", "-replace", "CFBundleURLTypes", "-json",
		`[{"CFBundleURLName":"Magnetk link","CFBundleURLSchemes":["magnetk"]}]`, plist); err != nil {
		return err
	}
	return h.run(lsregister, "-f", app)
}

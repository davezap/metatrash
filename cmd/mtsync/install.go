package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The service runs as the signed-in user, never as a system account: it
// needs the user's credentials and writes files the user owns. So it is
// started at login: a systemd user unit on Linux, a LaunchAgent on macOS and
// a Run key entry on Windows (which needs no administrator rights).

const systemdUnitName = "mtsync.service"
const launchAgentLabel = "com.metatrash.mtsync"
const windowsRunKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
const windowsRunValue = "Metatrash Sync"

func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func systemdUnit(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=Metatrash folder sync (mtsync)
After=network-online.target

[Service]
ExecStart=%q run
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, exe)
}

func launchAgent(exe string) string {
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + launchAgentLabel + `</string>
  <key>ProgramArguments</key>
  <array><string>` + escape(exe) + `</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
</dict>
</plist>
`
}

// windowsCommand is the login entry on Windows. mtsync start launches the
// service without a console window and returns at once.
func windowsCommand(exe string) string {
	return fmt.Sprintf(`"%s" start`, exe)
}

func command(out io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return nil
}

// install starts the service at every login and starts it now.
func install(ctx context.Context, out io.Writer) error {
	exe, err := executablePath()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		unit := filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
		if err := os.MkdirAll(filepath.Dir(unit), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(unit, []byte(systemdUnit(exe)), 0644); err != nil {
			return err
		}
		if err := command(out, "systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		if err := command(out, "systemctl", "--user", "enable", "--now", systemdUnitName); err != nil {
			return err
		}
		fmt.Fprintf(out, "Installed %s; it runs while you are logged in. To keep it running after logout: loginctl enable-linger %s\n", unit, os.Getenv("USER"))
	case "darwin":
		plist := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
		if err := os.MkdirAll(filepath.Dir(plist), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(plist, []byte(launchAgent(exe)), 0644); err != nil {
			return err
		}
		if err := command(out, "launchctl", "load", "-w", plist); err != nil {
			return err
		}
		fmt.Fprintf(out, "Installed %s; it starts at every login.\n", plist)
	case "windows":
		if err := command(io.Discard, "reg", "add", windowsRunKey, "/v", windowsRunValue, "/t", "REG_SZ", "/d", windowsCommand(exe), "/f"); err != nil {
			return err
		}
		fmt.Fprintln(out, "Installed: it starts at every login (Settings > Apps > Startup lists it as Metatrash Sync).")
		if err := startService(ctx, io.Discard); err != nil {
			return err
		}
	default:
		return fmt.Errorf("install is not supported on %s; run mtsync run from your own startup script", runtime.GOOS)
	}
	if err := waitForService(ctx); err != nil {
		return err
	}
	fmt.Fprintln(out, "The service is running. mtsync list shows the registered folders and mtsync log the service log.")
	return nil
}

// startService starts the service in the background if it is not running,
// through the login service manager where install set one up, and waits
// until it has started.
func startService(ctx context.Context, out io.Writer) error {
	if serviceRunning() {
		fmt.Fprintln(out, "The service is already running.")
		return nil
	}
	exe, err := executablePath()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	switch {
	case runtime.GOOS == "linux" && fileExists(filepath.Join(home, ".config", "systemd", "user", systemdUnitName)):
		err = command(out, "systemctl", "--user", "start", systemdUnitName)
	case runtime.GOOS == "darwin" && fileExists(filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")):
		err = command(out, "launchctl", "start", launchAgentLabel)
	default:
		err = startDetached(exe, "run")
	}
	if err != nil {
		return err
	}
	if err := waitForService(ctx); err != nil {
		return err
	}
	fmt.Fprintln(out, "The service is running. mtsync list shows the registered folders.")
	return nil
}

// waitForService waits for the service to take its lock, and says where to
// look when it does not.
func waitForService(ctx context.Context) error {
	deadline := time.Now().Add(10 * time.Second)
	for !serviceRunning() {
		if time.Now().After(deadline) {
			return fmt.Errorf("the service did not start; mtsync log shows why, or run mtsync run to see it in this window")
		}
		if err := sleepContext(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// uninstall stops starting the service at login and stops it now. Registered
// folders and their files stay.
func uninstall(ctx context.Context, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		unit := filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
		_ = command(out, "systemctl", "--user", "disable", "--now", systemdUnitName)
		os.Remove(unit)
		_ = command(out, "systemctl", "--user", "daemon-reload")
	case "darwin":
		plist := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
		_ = command(out, "launchctl", "unload", "-w", plist)
		os.Remove(plist)
	case "windows":
		_ = command(io.Discard, "reg", "delete", windowsRunKey, "/v", windowsRunValue, "/f")
	default:
		return fmt.Errorf("install is not supported on %s", runtime.GOOS)
	}
	if err := requestStop(ctx, io.Discard); err != nil {
		return err
	}
	fmt.Fprintln(out, "Uninstalled: the service is stopped and no longer starts at login. Registered folders are kept (mtsync list).")
	return nil
}

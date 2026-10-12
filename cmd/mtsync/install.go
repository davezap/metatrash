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

// windowsCommand starts the service without a console window (conhost
// --headless, Windows 10 1809 and later).
func windowsCommand(exe string) string {
	return fmt.Sprintf(`conhost.exe --headless "%s" run`, exe)
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
func install(out io.Writer) error {
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
		if err := command(out, "reg", "add", windowsRunKey, "/v", windowsRunValue, "/t", "REG_SZ", "/d", windowsCommand(exe), "/f"); err != nil {
			return err
		}
		if !serviceRunning() {
			if err := exec.Command("conhost.exe", "--headless", exe, "run").Start(); err != nil {
				return err
			}
		}
		fmt.Fprintln(out, "Installed: it starts at every login (Settings > Apps > Startup lists it as Metatrash Sync).")
	default:
		return fmt.Errorf("install is not supported on %s; run mtsync run from your own startup script", runtime.GOOS)
	}
	fmt.Fprintln(out, "mtsync list shows the registered folders; the log is mtsync.log beside the folder list (mtsync log).")
	return nil
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

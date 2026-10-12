// Command mtsync keeps folders on this computer in sync with metatrash
// spaces, so any editor or tool can work on the spaces' files. One program
// is both the background service, which syncs every registered folder, and
// the console that configures it. See README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"metatrash.com/metatrash"
)

const defaultServer = "https://metatrash.com"

const usage = `mtsync %s keeps folders in sync with metatrash spaces.

Usage: mtsync [flags] <command>

Set up:
  login                    connect this computer; choose spaces in the browser
  spaces                   list the spaces this computer may use
  logout                   forget this computer's sign-in

Folders (the service syncs every registered folder):
  add SPACE[/PATH] FOLDER  register FOLDER to sync with SPACE (owner/slug, or public),
                           or only with the folder PATH in it (owner/slug/site)
  remove FOLDER|SPACE      stop syncing a folder; its files stay
  list                     registered folders and how each is doing

Service:
  install                  run the service at every login, and start it now
  uninstall                stop it and no longer start it at login
  run                      run the service in this window (Ctrl+C to stop)
  stop                     stop the running service
  log [N]                  show the last N lines of the service log (default 40)

One-off:
  SPACE[/PATH] FOLDER      sync one folder in this window, without registering it
                           (-once: one pass, then exit)

Flags go before the command:
`

func main() {
	log.SetFlags(log.Ltime)
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("mtsync", flag.ContinueOnError)
	server := flags.String("server", defaultServer, "metatrash site (login, logout, spaces, add and one-off sync)")
	once := flags.Bool("once", false, "one-off sync: one pass, then exit")
	interval := flags.Duration("interval", 5*time.Second, "time between scans of each folder")
	keyEnv := flags.String("key-env", "", "one-off sync: name of an environment variable holding a space key, for spaces configured with keys instead of sign-in")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), usage, metatrash.Version)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	u, err := url.Parse(*server)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) || u.Host == "" || u.RawQuery != "" {
		return fmt.Errorf("-server must be an https URL")
	}
	base := strings.TrimSuffix(*server, "/")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rest := flags.Args()
	cmd := ""
	if len(rest) > 0 {
		cmd = rest[0]
	}
	switch {
	case cmd == "login" && len(rest) == 1:
		if err := login(ctx, base, os.Stdout); err != nil {
			return err
		}
		fmt.Println("Connected. Run mtsync spaces to see the spaces you chose, then mtsync add SPACE FOLDER.")
		return nil
	case cmd == "logout" && len(rest) == 1:
		if err := saveCredentials(base, nil); err != nil {
			return err
		}
		fmt.Println("Signed out on this computer. Remove the connection on Your account -> Services to revoke it on the site too.")
		return nil
	case cmd == "spaces" && len(rest) == 1:
		return listSpaces(ctx, base)
	case cmd == "add" && len(rest) == 3:
		return addFolder(ctx, base, rest[1], rest[2], os.Stdout)
	case cmd == "remove" && len(rest) == 2:
		return removeFolder(rest[1], os.Stdout)
	case (cmd == "list" || cmd == "status") && len(rest) == 1:
		return listFolders(os.Stdout)
	case cmd == "run" && len(rest) == 1:
		logFile, err := openLog("mtsync.log")
		if err != nil {
			return err
		}
		return runService(ctx, *interval, io.MultiWriter(os.Stderr, logFile))
	case cmd == "stop" && len(rest) == 1:
		return requestStop(ctx, os.Stdout)
	case cmd == "install" && len(rest) == 1:
		return install(os.Stdout)
	case cmd == "uninstall" && len(rest) == 1:
		return uninstall(ctx, os.Stdout)
	case cmd == "log" && len(rest) <= 2:
		lines := 40
		if len(rest) == 2 {
			if lines, err = strconv.Atoi(rest[1]); err != nil || lines < 1 {
				return fmt.Errorf("log takes a number of lines")
			}
		}
		return printLogTail(os.Stdout, lines)
	case cmd == "version" && len(rest) == 1:
		fmt.Println("mtsync", metatrash.Version)
		return nil
	case len(rest) == 2:
		return syncOneFolder(ctx, base, rest[0], rest[1], *keyEnv, *interval, *once)
	}
	flags.Usage()
	return flag.ErrHelp
}

// syncOneFolder syncs one folder in the foreground without registering it.
func syncOneFolder(ctx context.Context, server, space, folder, keyEnv string, interval time.Duration, once bool) error {
	var token func(context.Context, bool) (string, error)
	account := keyEnv == ""
	if account {
		token = tokenSource(server)
	} else {
		key := os.Getenv(keyEnv)
		if key == "" {
			return fmt.Errorf("environment variable %s is empty", keyEnv)
		}
		token = func(context.Context, bool) (string, error) { return key, nil }
	}
	c := newClient(server, space, token, account)
	s, err := openSyncer(c, folder, server, space, log.Printf)
	if err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if !once {
		log.Printf("Ctrl+C stops syncing")
	}
	return s.loop(ctx, interval, once)
}

func listSpaces(ctx context.Context, server string) error {
	c := newClient(server, "", tokenSource(server), true)
	c.base = strings.TrimSuffix(c.base, "/")
	var out struct {
		Spaces []struct {
			Space, Name, Access string
		} `json:"spaces"`
	}
	if err := c.do(ctx, "GET", "", nil, nil, &out); err != nil {
		return err
	}
	for _, s := range out.Spaces {
		access := "read and write"
		if s.Access == "read_only" {
			access = "read only"
		}
		fmt.Printf("%-40s %-15s %s\n", s.Space, access, s.Name)
	}
	return nil
}

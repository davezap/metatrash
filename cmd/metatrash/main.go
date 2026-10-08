package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"metatrash.com/metatrash"
	"metatrash.com/metatrash/internal/service"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		log.SetFlags(0)
		log.SetPrefix("metatrash: ")
		return runCommand(os.Args[1], os.Args[2:])
	}
	config := flag.String("config", "config/spaces.example.json", "Space configuration")
	keys := flag.String("keys", "", "Private key digest JSON file")
	accountsConfig := flag.String("accounts-config", os.Getenv("METATRASH_ACCOUNTS_CONFIG"), "Optional email account configuration file")
	data := flag.String("data", "data", "Persistent data directory")
	listen := flag.String("listen", "127.0.0.1:8080", "Loopback HTTP address")
	trusted := flag.String("trusted-proxies", "", "Comma-separated trusted proxy CIDRs; empty trusts none")
	publicURL := flag.String("public-url", "https://metatrash.com", "External HTTPS URL, optionally including a proxy-stripped path prefix")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("listen must be a loopback IP:port; expose via Apache HTTPS")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := service.Open(ctx, *config, *keys, *data, metatrash.SpaceREADME)
	if err != nil {
		return err
	}
	defer s.Close()
	if *accountsConfig != "" {
		if err := s.EnableAccounts(*accountsConfig); err != nil {
			return err
		}
	}
	proxies := []string{}
	if *trusted != "" {
		for _, value := range strings.Split(*trusted, ",") {
			proxies = append(proxies, strings.TrimSpace(value))
		}
	}
	handler, err := s.Handler(metatrash.ToolSchema, proxies, *publicURL)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	log.Printf("metatrash %s REST/MCP service on %s", metatrash.Version, *listen)
	select {
	case err := <-errCh:
		if err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
	}
	return nil
}

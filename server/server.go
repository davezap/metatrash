// Package server is the supported composition API for Metatrash applications.
package server

import (
	"context"
	"fmt"
	"io/fs"
	"metatrash.com/metatrash/extension"
	"net/http"
	"sync"

	"metatrash.com/metatrash"
	"metatrash.com/metatrash/internal/service"
)

// Config describes one server instance. Frontend is optional; when supplied,
// its root contains .vite/manifest.json and assets/. It is validated and
// snapshotted at construction, so editions never change process-global assets.
type Config struct {
	Modules        []extension.Module
	SpacesFile     string
	KeysFile       string
	DataDirectory  string
	AccountsFile   string
	PublicURL      string
	TrustedProxies []string
	Frontend       fs.FS
	// Optional application policy. Nil preserves Core's serial write worker.
	WriteScheduler extension.WriteSchedulerFactory
	// Optional trusted HTTP admission policy, copied at construction.
	// Nil preserves 2,400 global and 240 per-client-IP requests per minute.
	IngressLimits *IngressLimits
	// Optional bounded change-wait instrumentation; nil preserves Core behavior.
	ListWaitObserver extension.ListWaitObserver
}

// IngressLimits bounds all HTTP routes, independently of file-operation quotas.
type IngressLimits = service.IngressLimits

type Server struct {
	core    *service.Service
	handler http.Handler
	once    sync.Once
}

func Open(ctx context.Context, config Config) (*Server, error) {
	core, err := service.OpenWithWriteScheduler(ctx, config.SpacesFile, config.KeysFile, config.DataDirectory, metatrash.SpaceAGENTS, config.WriteScheduler)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			core.Close()
		}
	}()
	if err := core.SetModules(config.Modules); err != nil {
		return nil, err
	}
	if err := core.SetListWaitObserver(config.ListWaitObserver); err != nil {
		return nil, err
	}
	if len(config.Modules) > 0 && config.AccountsFile == "" {
		return nil, fmt.Errorf("extensions require accounts")
	}
	if config.Frontend != nil {
		if err := core.SetFrontend(config.Frontend); err != nil {
			return nil, err
		}
	}
	if config.AccountsFile != "" {
		if err := core.EnableAccounts(config.AccountsFile); err != nil {
			return nil, err
		}
	}
	origin := config.PublicURL
	if origin == "" {
		origin = "https://metatrash.com"
	}
	handler, err := core.HandlerWithIngressLimits(metatrash.ToolSchema, config.TrustedProxies, origin, config.IngressLimits)
	if err != nil {
		return nil, err
	}
	success = true
	return &Server{core: core, handler: handler}, nil
}
func (s *Server) Handler() http.Handler { return s.handler }

// Close must follow HTTP shutdown. Calling it repeatedly is safe.
func (s *Server) Close() { s.once.Do(s.core.Close) }

// Error is the stable HTTP problem returned by core operations.
type Error = service.Error

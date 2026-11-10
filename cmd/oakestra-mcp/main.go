// Command oakestra-mcp serves an MCP endpoint for an Oakestra deployment.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/robertjndw/oakestra-mcp/internal/actions"
	"github.com/robertjndw/oakestra-mcp/internal/activity"
	"github.com/robertjndw/oakestra-mcp/internal/auth"
	"github.com/robertjndw/oakestra-mcp/internal/candidates"
	"github.com/robertjndw/oakestra-mcp/internal/config"
	"github.com/robertjndw/oakestra-mcp/internal/httpapi"
	"github.com/robertjndw/oakestra-mcp/internal/mcpserver"
	"github.com/robertjndw/oakestra-mcp/internal/oakestra"
	"github.com/robertjndw/oakestra-mcp/internal/settings"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// One transport for everything that talks to Oakestra, so connections
	// to the system manager are reused across users.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 32
	transport.DialContext = (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	apiClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	oak, err := oakestra.NewFactory(cfg.OakestraAPIURL, apiClient, "oakestra-mcp/"+version)
	if err != nil {
		return err
	}
	authn := auth.NewAuthenticator(cfg.OakestraAPIURL, &http.Client{Transport: transport, Timeout: 10 * time.Second})
	cands := candidates.New(cfg.RootRAURL, cfg.ClusterRAPort, &http.Client{Transport: transport})

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	actStore, err := activity.Open(filepath.Join(cfg.DataDir, "activity.db"))
	if err != nil {
		return err
	}
	defer actStore.Close()
	// The secret is created even with writes disabled, so turning them on
	// later needs no extra step and the catalog can describe the tools.
	secret, err := actions.LoadOrCreateSecret(filepath.Join(cfg.DataDir, "secret"))
	if err != nil {
		return err
	}
	// MCP_ENABLE_WRITES only seeds the setting; once an Admin toggles it in
	// the dashboard, the stored value wins.
	st, err := settings.Open(filepath.Join(cfg.DataDir, "settings.json"),
		settings.Settings{WritesEnabled: cfg.EnableWrites})
	if err != nil {
		return err
	}
	engine := actions.NewEngine(actStore, secret, cfg.ConfirmTTL, cands)
	recorder := activity.NewRecorder(actStore)
	// Deferred after the store so it runs first and flushes queued records
	// before the database closes.
	defer recorder.Close()

	deps := mcpserver.Deps{
		Oakestra:   oak,
		Candidates: cands,
		Recorder:   recorder,
		Placement:  cands,
		Actions:    engine,
		Settings:   st,
		Version:    version,
	}
	handler := httpapi.New(httpapi.Options{
		Auth:            authn.Middleware(log),
		MCP:             mcpserver.Handler(deps),
		Tools:           mcpserver.Catalog(deps),
		Version:         version,
		PublicURL:       cfg.PublicURL,
		Settings:        st,
		ActivityHandler: actStore.Handler(),
		Logger:          log,
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// Generous enough for an explain that walks several clusters, while
		// still bounding a stuck upstream.
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go actStore.Run(ctx, cfg.ActivityRetention)

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr, "oakestra_api", cfg.OakestraAPIURL,
			"root_ra", cfg.RootRAURL, "cluster_ra_port", cfg.ClusterRAPort, "writes_enabled", st.WritesEnabled(),
			"version", version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

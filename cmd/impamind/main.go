//go:build !client

// impamind is a central persistent memory server for agents and people.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"impamind/internal/api"
	"impamind/internal/auth"
	"impamind/internal/authn"
	"impamind/internal/config"
	"impamind/internal/jobs"
	"impamind/internal/mcp"
	"impamind/internal/oauth"
	"impamind/internal/protocol"
	"impamind/internal/rag"
	"impamind/internal/store"
	"impamind/internal/web"
)

var version = "1.0.0"

func main() {
	protocol.Version = version
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := serve(); err != nil {
			slog.Error("serve", "err", err)
			os.Exit(1)
		}
	case "healthcheck":
		if err := healthcheck(); err != nil {
			os.Exit(1)
		}
	case "version":
		fmt.Println(version)
	case "hook", "sync":
		if err := clientMain(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: impamind serve | healthcheck | version | hook | sync")
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	lg := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(lg)

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()
	if err := validateRegistry(); err != nil {
		return err
	}
	probe, err := st.Probe(ctx)
	if err != nil {
		return fmt.Errorf("sqlite probe: %w", err)
	}
	if !probe.FTS5 || !probe.FTS5Porter {
		return fmt.Errorf("sqlite build lacks FTS5 porter unicode61 (fts5=%v porter=%v)", probe.FTS5, probe.FTS5Porter)
	}
	secret, generated, err := secretKey(cfg)
	if err != nil {
		return err
	}
	if generated {
		lg.Warn("IMPAMIND_SECRET not set; generated an ephemeral secret, web sessions will not survive restarts")
	}
	if err := bootstrap(ctx, st, cfg); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	if n, err := st.ProbeCount(ctx); err != nil {
		return err
	} else if n == 0 {
		if _, err := st.ProbeWrite(ctx, "boot"); err != nil {
			return fmt.Errorf("probe: %w", err)
		}
	}

	log := st.NewLogger()
	defer log.Close()
	svc := protocol.New(st, cfg, log)
	var index *rag.Index
	if cfg.RAG {
		index, err = rag.New(st, cfg, lg)
		if err != nil {
			return fmt.Errorf("rag: %w", err)
		}
		svc.Index = index
		go index.Run(ctx)
		lg.Info("rag enabled", "model", cfg.EmbedModel, "dims", cfg.EmbedDims)
	}
	an := authn.New(st, secret)
	oa := oauth.New(st, an, cfg, lg)
	an.OAuth = oa.Verify

	meta := stringsTrimRight(cfg.PublicURL, "/") + "/.well-known/oauth-protected-resource"
	agent, admin := mcp.Handlers(svc, an, mcp.Options{Versions: cfg.MCPVersions, ResourceMetadataURL: meta, Logger: lg})
	srv := api.New(api.Deps{Config: cfg, Store: st, Service: svc, Auth: an, Log: log, Slog: lg, Web: web.Handler(), MCP: agent, MCPAdm: admin})
	oa.Mount(srv)
	web.MountDownloads(srv)

	js := jobs.New(st, svc, cfg, lg)
	jobCtx, stopJobs := context.WithCancel(context.Background())
	defer stopJobs()
	go js.Run(jobCtx)

	httpSrv := &http.Server{Addr: cfg.Addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 120 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	lg.Info("impamind listening", "addr", cfg.Addr, "db", cfg.DBPath, "sqlite", probe.SQLiteVersion, "mcp", cfg.MCPVersions)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case s := <-sig:
		lg.Info("shutting down", "signal", s.String())
	}
	stopJobs()
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if index != nil {
		index.Stop()
	}
	return httpSrv.Shutdown(shutCtx)
}

func stringsTrimRight(s, cut string) string {
	for len(s) > 0 && len(cut) > 0 && s[len(s)-1:] == cut {
		s = s[:len(s)-1]
	}
	return s
}

func secretKey(cfg config.Config) ([]byte, bool, error) {
	if cfg.Secret != "" {
		sum := sha256.Sum256([]byte(cfg.Secret))
		return sum[:], false, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// bootstrap creates the token from IMPAMIND_BOOTSTRAP_TOKEN once. It is an
// agent token with read+propose on the global scope and read+write on every
// project; it can never approve.
func bootstrap(ctx context.Context, st *store.Store, cfg config.Config) error {
	if cfg.BootstrapToken == "" {
		return nil
	}
	global, _, err := st.ScopeRefs(ctx)
	if err != nil {
		return err
	}
	created, err := st.EnsureTokenHash(ctx, "bootstrap", "agent", auth.HashToken(cfg.BootstrapToken), []store.Grant{
		{ScopeID: global.ID, CanRead: true, CanPropose: true},
		{ScopeID: "*", CanRead: true, CanWrite: true, CanPropose: true},
	})
	if err != nil {
		return err
	}
	if created {
		slog.Info("bootstrap token created")
	}
	return nil
}

// validateRegistry refuses to start when a registered query is malformed.
func validateRegistry() error {
	for _, q := range store.Registry() {
		switch {
		case q.Name == "":
			return errors.New("query without name")
		case q.Scoped() && q.Raw == q.SQL:
			return fmt.Errorf("scoped query %s was not expanded", q.Name)
		case !q.Scoped() && !store.SystemReasons[q.Reason]:
			return fmt.Errorf("query %s has no scope predicate and no system reason", q.Name)
		}
	}
	return nil
}

func healthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	addr := cfg.Addr
	if len(addr) > 0 && addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/ready", nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return nil
}

// hex helper kept for debugging tokens in tests.
var _ = hex.EncodeToString

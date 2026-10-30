package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/docker"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/registry"
	"github.com/kolapsis/palier/internal/store"
	"github.com/kolapsis/palier/internal/web"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	logOut := os.Stdout
	if cmd != "serve" {
		logOut = os.Stderr
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(logOut, nil)))
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "scan":
		err = scanOnce()
	default:
		fmt.Fprintln(os.Stderr, "usage: palier [serve|scan]")
		os.Exit(2)
	}
	if err != nil {
		slog.Error("arrêt sur erreur", "err", err)
		os.Exit(1)
	}
}

type app struct {
	cfg config.Config
	db  *sql.DB
	dc  *docker.Client
	reg *registry.Client
}

func setup() (*app, error) {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	dc, err := docker.New(cfg.DockerHost)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &app{cfg: cfg, db: db, dc: dc, reg: registry.New(db)}, nil
}

func (a *app) scan(ctx context.Context) ([]*inventory.Stack, error) {
	stacks, err := inventory.Scan(ctx, a.dc)
	if err != nil {
		return nil, err
	}
	inventory.Detect(ctx, stacks, a.reg, a.cfg.Maturation)
	return stacks, nil
}

func scanOnce() error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.db.Close()
	stacks, err := a.scan(context.Background())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(stacks)
}

func serve() error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.db.Close()
	generated, err := a.cfg.EnsureToken()
	if err != nil {
		return err
	}
	if generated {
		slog.Info("jeton d'accès créé", "file", filepath.Join(a.cfg.DataDir, "token"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var mu sync.Mutex
	var snap web.Snapshot
	go func() {
		for {
			start := time.Now()
			stacks, err := a.scan(ctx)
			next := web.Snapshot{Stacks: stacks, At: time.Now()}
			if err != nil {
				next.Err = err.Error()
				slog.Error("inspection", "err", err)
			} else {
				slog.Info("inspection terminée", "stacks", len(stacks), "duration", time.Since(start).Round(time.Millisecond).String())
			}
			mu.Lock()
			snap = next
			mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(a.cfg.ScanInterval):
			}
		}
	}()
	current := func() web.Snapshot {
		mu.Lock()
		defer mu.Unlock()
		return snap
	}

	srv := &http.Server{Addr: a.cfg.Listen, Handler: web.New(a.cfg.Token, current), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("palier démarré", "listen", a.cfg.Listen, "data_dir", a.cfg.DataDir, "ai", a.cfg.AIKey != "", "auto", a.cfg.Auto)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("arrêt demandé")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

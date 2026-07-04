package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/webui"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// runUICmd implements `cpanel-self-migration ui`: a LOCAL web workstation
// over the pipeline artifacts in --dir. It renders the migration checklist
// (with staleness detection) and, interactively, lets the operator save the
// server connections (host.yaml) and launch the READ-ONLY analysis pipeline
// as a subprocess of this same binary — the CLI stays the single authority
// and --apply stays terminal-only. It binds to loopback only. Exit codes:
// 0 = clean shutdown, 1 = invalid input or serve failure, 2 = unparsable
// flags.
func runUICmd(args []string) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	dir := fs.String("dir", ".", "artifact directory to browse (where the pipeline wrote its JSON artifacts)")
	listen := fs.String("listen", "127.0.0.1:8422", "listen address — loopback only (127.0.0.1, ::1 or localhost)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration ui [--dir PATH] [--listen 127.0.0.1:8422]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// The base context is the parent of any in-flight analysis run; an
	// interrupt cancels it, so exec.CommandContext kills the SSH-connected
	// subprocess instead of orphaning it, and then the server drains.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := newUIServer(ctx, *dir, *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	go func() {
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		fmt.Fprintln(os.Stderr, "\ninterrupting — cancelling any running analysis and shutting down ...")
		cancel() // stop an in-flight run + its subprocess
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = srv.Shutdown(shutCtx)
	}()

	fmt.Fprintf(os.Stderr, "serving the migration dashboard for %s on http://%s/ — Ctrl-C to stop\n", *dir, *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// newUIServer validates the inputs and builds the (not yet listening)
// server. ctx becomes the parent of every analysis run. Split from
// runUICmd so the safety gates are unit-testable.
func newUIServer(ctx context.Context, dir, listen string) (*http.Server, error) {
	if err := webui.ValidateLoopback(listen); err != nil {
		return nil, err
	}
	opts := webui.Options{Dir: dir, BaseContext: ctx}
	// Enable workbench routes if the migration store exists or can be created.
	if store, err := workbench.NewStore(migrationHome()); err == nil {
		opts.SessionStore = store
	}
	h, err := webui.New(opts)
	if err != nil {
		return nil, err
	}
	return &http.Server{
		Addr:              listen,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}, nil
}

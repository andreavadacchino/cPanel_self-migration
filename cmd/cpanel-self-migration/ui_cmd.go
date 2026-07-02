package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/webui"
)

// runUICmd implements `cpanel-self-migration ui`: a LOCAL, read-only web
// dashboard over the pipeline artifacts in --dir (UI phase 1). It renders
// the migration checklist with staleness detection and the artifact
// presence table. It binds to loopback only, never opens SSH connections
// and never writes anything. Exit codes: 0 = clean shutdown, 1 = invalid
// input or serve failure, 2 = unparsable flags.
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

	srv, err := newUIServer(*dir, *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "serving the read-only dashboard for %s on http://%s/ — Ctrl-C to stop\n", *dir, *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// newUIServer validates the inputs and builds the (not yet listening)
// server. Split from runUICmd so the safety gates are unit-testable.
func newUIServer(dir, listen string) (*http.Server, error) {
	if err := webui.ValidateLoopback(listen); err != nil {
		return nil, err
	}
	h, err := webui.NewHandler(dir)
	if err != nil {
		return nil, err
	}
	return &http.Server{
		Addr:              listen,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}, nil
}

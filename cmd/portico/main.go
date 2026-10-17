// Command portico is the Portico appliance binary.
//
//	portico serve -config /etc/portico/portico.yaml
//	portico version
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/marco-fabbri/portico/internal/config"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "portico %s\n", version)
		return 0
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(stderr)
		path := fs.String("config", "/etc/portico/portico.yaml", "path to the settings file")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		cfg, err := config.Load(*path)
		if err != nil {
			fmt.Fprintf(stderr, "portico: %v\n", err)
			return 1
		}
		log := newLogger(stderr, cfg.Log.Level)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runServe(ctx, cfg, log); err != nil {
			fmt.Fprintf(stderr, "portico: %v\n", err)
			return 1
		}
		return 0
	default:
		usage(stderr)
		return 2
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: portico serve -config PATH | portico version")
}

// newLogger returns a JSON logger on w at the configured level.
func newLogger(w *os.File, level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv}))
}

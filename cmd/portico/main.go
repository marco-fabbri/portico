// Command portico is the Portico appliance binary.
//
//	portico serve -config /etc/portico/portico.yaml
//	portico admin set-password -config /etc/portico/portico.yaml   (password on stdin)
//	portico settings import -config /etc/portico/portico.yaml [-force]
//	portico settings show -config /etc/portico/portico.yaml
//	portico version
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/config"
	"github.com/marco-fabbri/portico/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "portico %s\n", version)
		return 0
	case "serve":
		cfg, ok := loadConfig("serve", args[1:], stderr)
		if !ok {
			return configExit(cfg)
		}
		log := newLogger(stderr, cfg.Log.Level)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return serveExitCode(runServe(ctx, cfg, log), stderr, log)
	case "settings":
		if len(args) < 2 {
			usage(stderr)
			return 2
		}
		switch args[1] {
		case "import":
			return settingsImport(args[2:], stdout, stderr)
		case "show":
			return settingsShow(args[2:], stdout, stderr)
		default:
			usage(stderr)
			return 2
		}
	case "console":
		cfg, ok := loadConfig("console", args[1:], stderr)
		if !ok {
			return configExit(cfg)
		}
		return runConsole(cfg, stdin, stdout)
	case "admin":
		if len(args) < 2 || args[1] != "set-password" {
			usage(stderr)
			return 2
		}
		cfg, ok := loadConfig("admin set-password", args[2:], stderr)
		if !ok {
			return configExit(cfg)
		}
		return setAdminPassword(cfg, stdin, stdout, stderr)
	default:
		usage(stderr)
		return 2
	}
}

// loadConfig parses "-config PATH" from args and loads the file. On a flag
// error the returned config is nil (exit 2); on a load error it is a
// non-nil empty config (exit 1), see configExit.
func loadConfig(name string, args []string, stderr io.Writer) (*config.Config, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "/etc/portico/portico.yaml", "path to the settings file")
	if err := fs.Parse(args); err != nil {
		return nil, false
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return &config.Config{}, false
	}
	return cfg, true
}

func configExit(cfg *config.Config) int {
	if cfg == nil {
		return 2
	}
	return 1
}

// setAdminPassword reads one line from stdin and stores its bcrypt hash as
// the break-glass account "admin". The password never reaches a flag, the
// environment or a log line.
func setAdminPassword(cfg *config.Config, stdin io.Reader, stdout, stderr io.Writer) int {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		fmt.Fprintf(stderr, "portico: read password: %v\n", err)
		return 1
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		fmt.Fprintln(stderr, "portico: password is empty (write it on stdin, one line)")
		return 2
	}
	st, err := store.OpenSQLite(cfg.Store.Path)
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	defer st.Close()
	// The same path as the password page: a password set by hand is a
	// chosen one, so it clears the forced change at login.
	if err := api.SetAdminPassword(context.Background(), st, pw); err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "password set for admin")
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: portico serve -config PATH | portico console -config PATH | portico admin set-password -config PATH | portico settings import|show -config PATH | portico version")
}

// newLogger returns a JSON logger on w at the configured level.
func newLogger(w io.Writer, level string) *slog.Logger {
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

// serveExitCode maps runServe's result to the process exit code. A
// requested restart exits 0 so systemd's Restart=always starts the
// service again without counting a failure.
func serveExitCode(err error, stderr io.Writer, log *slog.Logger) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrRestart):
		log.Info("exiting for the restart")
		return 0
	default:
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
}

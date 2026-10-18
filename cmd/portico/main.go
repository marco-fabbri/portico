// Command portico is the Portico appliance binary.
//
//	portico serve -config /etc/portico/portico.yaml
//	portico admin set-password -config /etc/portico/portico.yaml   (password on stdin)
//	portico version
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/marco-fabbri/portico/internal/config"
	"github.com/marco-fabbri/portico/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// bcryptCost is the work factor of the break-glass password hash.
const bcryptCost = 12

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
		if err := runServe(ctx, cfg, log); err != nil {
			fmt.Fprintf(stderr, "portico: %v\n", err)
			return 1
		}
		return 0
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
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	st, err := store.OpenSQLite(cfg.Store.Path)
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	defer st.Close()
	if err := st.SetAdminPassword(context.Background(), "admin", string(hash)); err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "password set for admin")
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: portico serve -config PATH | portico admin set-password -config PATH | portico version")
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

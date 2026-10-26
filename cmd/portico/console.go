package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/marco-fabbri/portico/internal/config"
	"github.com/marco-fabbri/portico/internal/console"
	"github.com/marco-fabbri/portico/internal/exposure"
)

// runConsole is `portico console`: the wizard on tty1, run as root by the
// getty override (deploy/portico-console.service).
func runConsole(cfg *config.Config, stdin io.Reader, stdout io.Writer) int {
	console.Version = version
	if _, port, err := net.SplitHostPort(cfg.Internal.AdminListen); err == nil {
		console.AdminPort = port
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sys := &hostSystem{tty: isTerminal(stdin)}
	if err := console.Run(ctx, stdin, stdout, sys); err != nil {
		fmt.Fprintf(stdout, "portico: %v\n", err)
		return 1
	}
	return 0
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// hostSystem is console.System on a real appliance: sysfs for the
// interfaces, NetworkManager keyfiles, nmcli, systemctl, the admin
// listener over the loopback.
type hostSystem struct {
	tty bool
}

const (
	sysNet       = "/sys/class/net"
	nmConnection = "/etc/NetworkManager/system-connections"
)

func (h *hostSystem) Interfaces() ([]console.Interface, error) {
	entries, err := os.ReadDir(sysNet)
	if err != nil {
		return nil, err
	}
	var out []console.Interface
	for _, e := range entries {
		name := e.Name()
		if name == "lo" {
			continue
		}
		// Virtual devices (bridges, podman veths) live under /sys/devices/virtual.
		if target, err := filepath.EvalSymlinks(filepath.Join(sysNet, name)); err == nil && strings.Contains(target, "/devices/virtual/") {
			continue
		}
		mac, _ := os.ReadFile(filepath.Join(sysNet, name, "address"))
		state, _ := os.ReadFile(filepath.Join(sysNet, name, "operstate"))
		out = append(out, console.Interface{Name: name, MAC: strings.TrimSpace(string(mac)), Up: strings.TrimSpace(string(state)) == "up"})
	}
	return out, nil
}

func keyfilePath(name string) string { return filepath.Join(nmConnection, name+".nmconnection") }

func (h *hostSystem) ReadKeyfile(name string) (console.NetConfig, bool, error) {
	b, err := os.ReadFile(keyfilePath(name))
	if errors.Is(err, os.ErrNotExist) {
		return console.NetConfig{}, false, nil
	}
	if err != nil {
		return console.NetConfig{}, false, err
	}
	c, err := console.ParseKeyfile(string(b))
	if err != nil {
		return console.NetConfig{}, false, nil
	}
	if hn, err := os.Hostname(); err == nil {
		c.Hostname = hn
	}
	return c, true, nil
}

func (h *hostSystem) WriteKeyfile(name string, c console.NetConfig) error {
	if err := os.MkdirAll(nmConnection, 0o755); err != nil {
		return err
	}
	// NetworkManager refuses keyfiles that are not 0600 root.
	return os.WriteFile(keyfilePath(name), []byte(console.Keyfile(name, c)), 0o600)
}

func (h *hostSystem) Apply(name string) error {
	if _, err := h.Exec(nil, "nmcli", "connection", "reload"); err != nil {
		return err
	}
	_, err := h.Exec(nil, "nmcli", "connection", "up", name)
	return err
}

func (h *hostSystem) Health(url string) (string, error) {
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // #nosec G402: the appliance's own listener, over its own address
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(b), nil
}

func (h *hostSystem) Unit(name string) (string, error) {
	out, _ := exec.Command("systemctl", "is-active", name).Output()
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "unknown", nil
	}
	return s, nil
}

func (h *hostSystem) Exec(stdin io.Reader, argv ...string) (string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("%s: %s", argv[0], msg)
	}
	return out.String(), nil
}

func (h *hostSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (h *hostSystem) Remove(path string) error { return os.Remove(path) }

// ReadRequestFile reads a file of the exposure request directory, which
// the unprivileged portico user owns, for `portico exposure apply` run as
// root: never through a link (O_NOFOLLOW), never a FIFO or a device (the
// open does not block, then the file must be regular), at most
// exposure.MaxRequestFile bytes.
func (h *hostSystem) ReadRequestFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a link, not a regular file", filepath.Base(path))
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	b, err := io.ReadAll(io.LimitReader(f, exposure.MaxRequestFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > exposure.MaxRequestFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), exposure.MaxRequestFile)
	}
	return b, nil
}

// WriteResult writes the result of an exposure request beside it: a
// fresh temporary file renamed over the old one, so a link planted under
// that name is replaced, never followed.
func (h *hostSystem) WriteResult(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".result-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (h *hostSystem) WriteFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	// Files under /etc/portico are read by the portico user (group).
	if strings.HasPrefix(path, "/etc/portico/") {
		_, _ = h.Exec(nil, "chgrp", "portico", path)
	}
	return nil
}

// Echo turns the terminal echo off while a secret is typed.
func (h *hostSystem) Echo(on bool) {
	if !h.tty {
		return
	}
	arg := "echo"
	if !on {
		arg = "-echo"
	}
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}

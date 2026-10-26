package exposure

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	CertPath     = "/etc/portico/tls/public.crt"
	KeyPath      = "/etc/portico/tls/public.key"
	TokenPath    = "/etc/cloudflared/token"
	tunnelListen = "127.0.0.1:8081" // 8080 is Keycloak's published port
	directListen = ":443"
)

// The files of a request in the exposure directory. The admin UI writes
// the pair or the token first and request.json last: the path unit
// watches request.json.
const (
	RequestFile = "request.json"
	ResultFile  = "result.json"
	CertFile    = "public.crt"
	KeyFile     = "public.key"
	TokenFile   = "token"
)

// Request is what the admin UI asks for.
type Request struct {
	ID         string `json:"id"`
	Mode       string `json:"mode"` // "direct" | "tunnel"
	SelfSigned bool   `json:"self_signed,omitempty"`
}

// Result is what Apply did, for the admin UI to show.
type Result struct {
	ID      string    `json:"id"`
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	Mode    string    `json:"mode"`
	At      time.Time `json:"at"`
}

// Host is what Apply does to the appliance: the console's System subset,
// and two calls for the request directory, which belongs to the
// unprivileged portico user: ReadRequestFile reads a regular file only,
// never through a link and at most MaxRequestFile bytes; WriteResult
// replaces the file by a rename, so a link planted there is replaced,
// not followed.
type Host interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, mode os.FileMode) error
	Remove(path string) error
	Exec(stdin io.Reader, argv ...string) (string, error)
	ReadRequestFile(path string) ([]byte, error)
	WriteResult(path string, data []byte) error
}

// MaxRequestFile bounds what Apply reads from the request directory.
const MaxRequestFile = 64 << 10

// How long cloudflared gets to fail after its start: a malformed or
// revoked token makes it exit within a second or two, and systemd's
// Restart=always turns that into restarts.
var (
	settle = 5 * time.Second
	sleep  = time.Sleep
)

// Apply runs as root from the path unit: it reads the request in dir,
// installs the pair or the token, switches the process YAML at
// configPath and cloudflared when the mode changes, and restarts Portico
// only then (a renewal is picked up by the listener's reload). The
// request files are deleted whatever the outcome, and the result is
// written to result.json. Without a request it does nothing: the unit
// fires again when the request is deleted.
func Apply(h Host, dir, configPath string) Result {
	b, err := h.ReadRequestFile(filepath.Join(dir, RequestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}
	}
	var req Request
	r := Result{At: time.Now().UTC()}
	if err != nil {
		r.Message = "the request cannot be read: " + err.Error()
	} else if err := json.Unmarshal(b, &req); err != nil {
		r.Message = "the request cannot be read: " + err.Error()
	} else {
		r.ID, r.Mode = req.ID, req.Mode
		r.Message, r.OK = apply(h, dir, configPath, req)
	}
	// The result first: the admin UI stops waiting when request.json is
	// gone or the result carries its id.
	out, _ := json.Marshal(r)
	_ = h.WriteResult(filepath.Join(dir, ResultFile), out)
	removed := true
	for _, f := range []string{RequestFile, CertFile, KeyFile, TokenFile} {
		if err := h.Remove(filepath.Join(dir, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			r.Message += "; " + f + " not deleted: " + err.Error()
			removed = false
		}
	}
	if !removed {
		out, _ = json.Marshal(r)
		_ = h.WriteResult(filepath.Join(dir, ResultFile), out)
	}
	return r
}

func apply(h Host, dir, configPath string, req Request) (string, bool) {
	y, err := h.ReadFile(configPath)
	if err != nil {
		return err.Error(), false
	}
	wasTunnel := tunnelMode(string(y))
	switch req.Mode {
	case "direct":
		return direct(h, dir, configPath, string(y), wasTunnel)
	case "tunnel":
		return tunnel(h, dir, configPath, string(y), wasTunnel)
	default:
		return fmt.Sprintf("unknown mode %q", req.Mode), false
	}
}

func direct(h Host, dir, configPath, y string, wasTunnel bool) (string, bool) {
	cert, err := h.ReadRequestFile(filepath.Join(dir, CertFile))
	if err != nil {
		return "no certificate in the request: " + err.Error(), false
	}
	key, err := h.ReadRequestFile(filepath.Join(dir, KeyFile))
	if err != nil {
		return "no key in the request: " + err.Error(), false
	}
	// The admin UI has checked the pair; root checks it again, since the
	// files come from an unprivileged process.
	if _, err := tls.X509KeyPair(cert, key); err != nil {
		return "the key does not match the certificate, or the files are not PEM", false
	}
	text := y
	if wasTunnel {
		if text, err = setMode(y, false); err != nil {
			return err.Error(), false
		}
	}
	// The key first: the listener reloads when the certificate changes.
	if err := h.WriteFile(KeyPath, key, 0o640); err != nil {
		return err.Error(), false
	}
	if err := h.WriteFile(CertPath, cert, 0o644); err != nil {
		return err.Error(), false
	}
	if !wasTunnel {
		return "certificate installed; the public listener serves it within a few seconds", true
	}
	if err := h.WriteFile(configPath, []byte(text), 0o640); err != nil {
		return err.Error(), false
	}
	// cloudflared may be absent (install.sh tolerates it): stopping it is best effort.
	h.Exec(nil, "systemctl", "disable", "--now", "cloudflared")
	if _, err := h.Exec(nil, "systemctl", "restart", "portico"); err != nil {
		return "portico did not restart: " + err.Error(), false
	}
	return "direct exposure on port 443 with the installed certificate; Portico restarted", true
}

func tunnel(h Host, dir, configPath, y string, wasTunnel bool) (string, bool) {
	token, err := h.ReadRequestFile(filepath.Join(dir, TokenFile))
	if err != nil {
		return "no token in the request: " + err.Error(), false
	}
	text := y
	if !wasTunnel {
		if text, err = setMode(y, true); err != nil {
			return err.Error(), false
		}
	}
	old, oldErr := h.ReadFile(TokenPath)
	if err := h.WriteFile(TokenPath, token, 0o600); err != nil {
		return err.Error(), false
	}
	// cloudflared first: when it cannot run (not installed, bad token)
	// Portico keeps the exposure it had.
	if msg := startCloudflared(h); msg != "" {
		if oldErr == nil {
			h.WriteFile(TokenPath, old, 0o600)
		} else {
			h.Remove(TokenPath)
		}
		if wasTunnel {
			h.Exec(nil, "systemctl", "restart", "cloudflared")
		} else {
			h.Exec(nil, "systemctl", "disable", "--now", "cloudflared")
		}
		return msg + "; Portico keeps its current exposure", false
	}
	if wasTunnel {
		return "tunnel token replaced; cloudflared restarted", true
	}
	if err := h.WriteFile(configPath, []byte(text), 0o640); err != nil {
		return err.Error(), false
	}
	if _, err := h.Exec(nil, "systemctl", "restart", "portico"); err != nil {
		return "portico did not restart: " + err.Error(), false
	}
	return "tunnel mode: Portico listens on the loopback for cloudflared; Portico restarted", true
}

// startCloudflared starts the connector and waits for it to stay up;
// the message is empty on success.
func startCloudflared(h Host) string {
	if _, err := h.Exec(nil, "systemctl", "enable", "cloudflared"); err != nil {
		return "cloudflared cannot be enabled: " + err.Error()
	}
	if _, err := h.Exec(nil, "systemctl", "restart", "cloudflared"); err != nil {
		return "cloudflared did not start: " + err.Error()
	}
	sleep(settle)
	out, err := h.Exec(nil, "systemctl", "show", "-p", "ActiveState", "-p", "NRestarts", "cloudflared")
	if err != nil {
		return "cloudflared state unknown: " + err.Error()
	}
	if !strings.Contains(out, "ActiveState=active") || !strings.Contains(out, "NRestarts=0") {
		return "cloudflared stopped right after its start: the token is probably wrong (journalctl -u cloudflared)"
	}
	return ""
}

var (
	listenRe = regexp.MustCompile(`(?m)^(\s*listen:\s*)"[^"]*"`)
	certRe   = regexp.MustCompile(`(?m)^(\s*cert_file:\s*)\S*`)
	keyRe    = regexp.MustCompile(`(?m)^(\s*key_file:\s*)\S*`)
)

// tunnelMode reports whether the process YAML's public listener is the
// loopback one of the tunnel.
func tunnelMode(y string) bool {
	m := listenRe.FindStringSubmatch(y)
	return m != nil && strings.Contains(m[0], tunnelListen)
}

// setMode rewrites the three public lines of the process YAML, comments
// kept: tunnel mode listens on the loopback with no TLS files, direct
// mode on :443 with the installed pair. Only the first occurrence of
// each key is touched (the public block comes first in the file).
func setMode(y string, tunnel bool) (string, error) {
	listen, cert, key := directListen, CertPath, KeyPath
	if tunnel {
		listen, cert, key = tunnelListen, `""`, `""`
	}
	text := y
	for _, r := range []struct {
		re   *regexp.Regexp
		repl string
		key  string
	}{{listenRe, `${1}"` + listen + `"`, "public.listen"}, {certRe, "${1}" + cert, "public.tls.cert_file"}, {keyRe, "${1}" + key, "public.tls.key_file"}} {
		var ok bool
		if text, ok = replaceFirst(r.re, text, r.repl); !ok {
			return y, fmt.Errorf("%s not found in the process settings in the shape Portico edits (a quoted value); edit the file by hand", r.key)
		}
	}
	return text, nil
}

func replaceFirst(re *regexp.Regexp, text, repl string) (string, bool) {
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return text, false
	}
	var dst []byte
	dst = re.ExpandString(dst, repl, text, loc)
	return text[:loc[0]] + string(dst) + text[loc[1]:], true
}

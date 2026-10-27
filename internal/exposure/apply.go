package exposure

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/textedit"
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
// and OpenRequestDir for the request directory, which belongs to the
// unprivileged portico user and is opened once: a link planted in its
// place, before or during Apply, never makes root work elsewhere.
type Host interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, mode os.FileMode) error
	Remove(path string) error
	Exec(stdin io.Reader, argv ...string) (string, error)
	OpenRequestDir(dir string) (RequestDir, error)
}

// RequestDir is the opened request directory. Read returns a regular
// file only, never through a link and at most MaxRequestFile bytes;
// WriteResult replaces the file by a rename, so a link planted under
// that name is replaced, not followed.
type RequestDir interface {
	Read(name string) ([]byte, error)
	WriteResult(name string, data []byte) error
	Remove(name string) error
	Close() error
}

// MaxRequestFile bounds what Apply reads from the request directory.
const MaxRequestFile = 64 << 10

// How long cloudflared gets to register a connection with Cloudflare
// after its start (about eight seconds, measured), and how often its
// readiness endpoint is asked. A token Cloudflare does not know does not
// make cloudflared exit: it keeps retrying, so only a registered
// connection proves the token.
const (
	tunnelWait = 30 * time.Second
	tunnelPoll = time.Second
	// tunnelReadyURL is cloudflared's readiness endpoint, on the metrics
	// address deploy/cloudflared.service pins: 200 once a connection is
	// registered, 503 before.
	tunnelReadyURL = "http://127.0.0.1:20241/ready"
)

var (
	sleep       = time.Sleep
	tunnelReady = func() bool {
		c := http.Client{Timeout: 2 * time.Second}
		resp, err := c.Get(tunnelReadyURL)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
)

// Apply runs as root from the path unit: it reads the request in dir,
// installs the pair or the token, switches the process YAML at
// configPath and cloudflared when the mode changes, and restarts Portico
// only then (a renewal is picked up by the listener's reload). The
// request files are deleted whatever the outcome, and the result is
// written to result.json. Without a request it does nothing: the unit
// fires again when the request is deleted.
func Apply(h Host, dir, configPath string) Result {
	d, err := h.OpenRequestDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}
	}
	if err != nil {
		return Result{At: time.Now().UTC(), Message: "the request directory cannot be opened: " + err.Error()}
	}
	defer d.Close()
	b, err := d.Read(RequestFile)
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
		r.Message, r.OK = apply(h, d, configPath, req)
	}
	// The result first: the admin UI stops waiting when request.json is
	// gone or the result carries its id.
	out, _ := json.Marshal(r)
	_ = d.WriteResult(ResultFile, out)
	removed := true
	for _, f := range []string{RequestFile, CertFile, KeyFile, TokenFile} {
		if err := d.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			r.Message += "; " + f + " not deleted: " + err.Error()
			removed = false
		}
	}
	if !removed {
		out, _ = json.Marshal(r)
		_ = d.WriteResult(ResultFile, out)
	}
	return r
}

func apply(h Host, d RequestDir, configPath string, req Request) (string, bool) {
	y, err := h.ReadFile(configPath)
	if err != nil {
		return err.Error(), false
	}
	wasTunnel := tunnelMode(string(y))
	switch req.Mode {
	case "direct":
		return direct(h, d, configPath, string(y), wasTunnel)
	case "tunnel":
		return tunnel(h, d, configPath, string(y), wasTunnel)
	default:
		return fmt.Sprintf("unknown mode %q", req.Mode), false
	}
}

func direct(h Host, d RequestDir, configPath, y string, wasTunnel bool) (string, bool) {
	cert, err := d.Read(CertFile)
	if err != nil {
		return "no certificate in the request: " + err.Error(), false
	}
	key, err := d.Read(KeyFile)
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

func tunnel(h Host, d RequestDir, configPath, y string, wasTunnel bool) (string, bool) {
	token, err := d.Read(TokenFile)
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

// startCloudflared starts the connector and waits for Cloudflare to
// register its connection; the message is empty on success.
func startCloudflared(h Host) string {
	if _, err := h.Exec(nil, "systemctl", "enable", "cloudflared"); err != nil {
		return "cloudflared cannot be enabled: " + err.Error()
	}
	if _, err := h.Exec(nil, "systemctl", "restart", "cloudflared"); err != nil {
		return "cloudflared did not start: " + err.Error()
	}
	for waited := time.Duration(0); waited < tunnelWait; waited += tunnelPoll {
		if tunnelReady() {
			return ""
		}
		sleep(tunnelPoll)
	}
	return "cloudflared did not register a connection with Cloudflare within 30 seconds: the token is probably wrong or revoked (journalctl -u cloudflared)"
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
		if text, ok = textedit.ReplaceFirst(r.re, text, r.repl); !ok {
			return y, fmt.Errorf("%s not found in the process settings in the shape Portico edits (a quoted value); edit the file by hand", r.key)
		}
	}
	return text, nil
}

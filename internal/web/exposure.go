package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/exposure"
)

// ExposureOptions locates the exposure requests and the installed public
// certificate. The admin UI writes a request; portico-exposure.path
// applies it as root (internal/exposure).
type ExposureOptions struct {
	Dir      string // the request directory; empty hides the exposure form
	CertFile string // the public certificate in service
	Tunnel   bool   // the process runs in tunnel mode
}

// maxExposureFile is the limit of each uploaded file: a full chain with a
// few intermediates is under 10 KiB.
const maxExposureFile = 64 << 10

// exposureData is the exposure fragment: the mode and certificate in
// service, the last result, and whether a request is being applied.
type exposureData struct {
	Enabled bool
	Tunnel  bool
	Cert    *exposure.Info
	Last    *exposure.Result
	Pending bool
	// Restart: the pending request changes the mode, so Portico restarts
	// and this session ends.
	Restart bool
	Error   string
	CSRF    string
}

func (h *Handler) exposureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /exposure", h.exposureSubmit)
	mux.HandleFunc("GET /exposure/status", h.exposureStatus)
}

func (h *Handler) exposureState(r *http.Request) exposureData {
	o := h.exposure
	d := exposureData{Enabled: o.Dir != "", Tunnel: o.Tunnel}
	if s, ok := h.session(r); ok {
		d.CSRF = s.CSRF
	}
	if !d.Enabled {
		return d
	}
	if !o.Tunnel && o.CertFile != "" {
		if b, err := os.ReadFile(o.CertFile); err == nil {
			if info, err := exposure.Inspect(b); err == nil {
				d.Cert = &info
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(o.Dir, exposure.ResultFile)); err == nil {
		var res exposure.Result
		if json.Unmarshal(b, &res) == nil {
			d.Last = &res
		}
	}
	if b, err := os.ReadFile(filepath.Join(o.Dir, exposure.RequestFile)); err == nil {
		d.Pending = true
		var req exposure.Request
		if json.Unmarshal(b, &req) == nil {
			d.Restart = (req.Mode == "tunnel") != o.Tunnel
		}
	}
	return d
}

func (h *Handler) renderExposure(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	d := h.exposureState(r)
	d.Error = errMsg
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.pages["setup"].ExecuteTemplate(w, "exposure-status", d); err != nil {
		h.log.Error("template", "page", "setup/exposure", "err", err.Error())
	}
}

func (h *Handler) exposureStatus(w http.ResponseWriter, r *http.Request) {
	h.renderExposure(w, r, http.StatusOK, "")
}

// exposureSubmit checks what the form carries and writes a request: the
// pair or the token first, request.json last (the path unit watches it).
// Nothing is written when a check fails, and no secret is echoed or
// logged.
func (h *Handler) exposureSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 3*maxExposureFile)
	if err := r.ParseMultipartForm(3 * maxExposureFile); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			h.renderExposure(w, r, http.StatusRequestEntityTooLarge, "a file is larger than 64 KiB: upload the PEM certificate chain and key only")
			return
		}
		h.renderExposure(w, r, http.StatusBadRequest, "the form cannot be read")
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	dir := h.exposure.Dir
	if dir == "" || h.setup == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(filepath.Join(dir, exposure.RequestFile)); err == nil {
		h.renderExposure(w, r, http.StatusConflict, "a change is being applied: wait for its result")
		return
	}
	cur, _, ok := h.current(w, r)
	if !ok {
		return
	}
	if cur.PortalHost == "" || cur.IDHost == "" {
		h.renderExposure(w, r, http.StatusConflict, "save the public names first: the certificate must name both")
		return
	}
	hosts := []string{cur.PortalHost, cur.IDHost}
	req := exposure.Request{ID: newRequestID()}
	files := map[string][]byte{}
	mode := r.FormValue("mode")
	switch mode {
	case "certificate", "selfsigned":
		req.Mode = "direct"
		var cert, key []byte
		if mode == "selfsigned" {
			var err error
			if cert, key, err = exposure.SelfSigned(hosts); err != nil {
				h.renderExposure(w, r, http.StatusInternalServerError, err.Error())
				return
			}
			req.SelfSigned = true
		} else {
			var status int
			var msg string
			if cert, status, msg = uploadedFile(r, "cert"); msg != "" {
				h.renderExposure(w, r, status, "certificate: "+msg)
				return
			}
			if key, status, msg = uploadedFile(r, "key"); msg != "" {
				h.renderExposure(w, r, status, "key: "+msg)
				return
			}
			if _, err := exposure.Validate(cert, key, hosts, time.Now()); err != nil {
				h.renderExposure(w, r, http.StatusBadRequest, err.Error())
				return
			}
		}
		files[exposure.KeyFile], files[exposure.CertFile] = key, cert
	case "tunnel":
		req.Mode = "tunnel"
		token := strings.TrimSpace(r.FormValue("token"))
		if err := exposure.ValidateToken(token); err != nil {
			h.renderExposure(w, r, http.StatusBadRequest, err.Error())
			return
		}
		files[exposure.TokenFile] = []byte(token + "\n")
	default:
		h.renderExposure(w, r, http.StatusBadRequest, "choose how the public names are published")
		return
	}
	for _, name := range []string{exposure.KeyFile, exposure.CertFile, exposure.TokenFile} {
		if b, ok := files[name]; ok {
			if err := writePrivate(filepath.Join(dir, name), b); err != nil {
				h.log.Error("exposure request not written", "file", name, "err", err.Error())
				h.renderExposure(w, r, http.StatusInternalServerError, "the request cannot be written: "+err.Error())
				return
			}
		}
	}
	b, _ := json.Marshal(req)
	if err := writePrivate(filepath.Join(dir, exposure.RequestFile), b); err != nil {
		h.log.Error("exposure request not written", "file", exposure.RequestFile, "err", err.Error())
		h.renderExposure(w, r, http.StatusInternalServerError, "the request cannot be written: "+err.Error())
		return
	}
	h.log.Info("exposure requested", "via", "ui", "id", req.ID, "mode", req.Mode, "self_signed", req.SelfSigned)
	h.renderExposure(w, r, http.StatusOK, "")
}

// uploadedFile reads one uploaded file of at most maxExposureFile bytes; the
// message is empty on success.
func uploadedFile(r *http.Request, name string) ([]byte, int, string) {
	f, _, err := r.FormFile(name)
	if err != nil {
		return nil, http.StatusBadRequest, "no file chosen"
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxExposureFile+1))
	if err != nil {
		return nil, http.StatusBadRequest, "the file cannot be read"
	}
	if len(b) > maxExposureFile {
		return nil, http.StatusRequestEntityTooLarge, "larger than 64 KiB"
	}
	return b, 0, ""
}

func writePrivate(path string, b []byte) error {
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func newRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

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
	mux.HandleFunc("GET /exposure/ready", h.exposureReady)
}

// staleRequest is how long a request may wait for the path unit: Apply
// takes seconds (the tunnel check waits five), so a request still there
// after two minutes was never applied, and must not lock the form.
const staleRequest = 2 * time.Minute

// pendingRequest reports the request waiting in dir: pending while it is
// there without a result carrying its id, stale once it has waited
// longer than staleRequest.
func pendingRequest(dir string) (req exposure.Request, pending, stale bool) {
	path := filepath.Join(dir, exposure.RequestFile)
	fi, err := os.Stat(path)
	if err != nil {
		return req, false, false
	}
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &req)
	if b, err := os.ReadFile(filepath.Join(dir, exposure.ResultFile)); err == nil {
		var res exposure.Result
		if json.Unmarshal(b, &res) == nil && req.ID != "" && res.ID == req.ID {
			return req, false, false
		}
	}
	if time.Since(fi.ModTime()) > staleRequest {
		return req, false, true
	}
	return req, true, false
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
	req, pending, stale := pendingRequest(o.Dir)
	switch {
	case pending:
		d.Pending = true
		d.Restart = (req.Mode == "tunnel") != o.Tunnel
	case stale:
		d.Error = "the last change was not applied within two minutes: check systemctl status portico-exposure.path on the appliance, then send it again"
	}
	return d
}

func (h *Handler) renderExposure(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	d := h.exposureState(r)
	if errMsg != "" {
		d.Error = errMsg
	}
	h.writeTemplate(w, swapStatus(r, status), "setup", "exposure-status", d)
}

// swapStatus is the status of a fragment answer: HTMX 2 swaps only 2xx
// responses, so an HTMX request gets a refusal with 200 and the message
// in the fragment; other clients get the real status.
func swapStatus(r *http.Request, status int) int {
	if r.Header.Get("HX-Request") == "true" {
		return http.StatusOK
	}
	return status
}

func (h *Handler) exposureStatus(w http.ResponseWriter, r *http.Request) {
	h.renderExposure(w, r, http.StatusOK, "")
}

const exposureReadyBanner = `<div class="banner warning" hx-get="/exposure/ready" hx-trigger="every 2s" hx-headers='{"X-Portico-Poll": "1"}' hx-disinherit="hx-headers" hx-swap="outerHTML">Applying: if the mode changes, Portico restarts and this session ends.</div>`

// exposureReady is what the restart banner polls: a mode change restarts
// Portico and the session with it, so this answer needs no session and
// says only whether a change is still being applied, nothing of it.
func (h *Handler) exposureReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.exposure.Dir == "" {
		http.NotFound(w, r)
		return
	}
	if _, pending, _ := pendingRequest(h.exposure.Dir); pending {
		w.Write([]byte(exposureReadyBanner))
		return
	}
	w.Write([]byte(`<div class="banner notice">Done. <a href="/setup">Open the setup page</a> to see the result; after a restart it asks you to log in again.</div>`))
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
	if old, pending, stale := pendingRequest(dir); pending {
		h.renderExposure(w, r, http.StatusConflict, "a change is being applied: wait for its result")
		return
	} else if stale {
		// Nobody applied it: its files go before the new ones are written.
		for _, f := range []string{exposure.RequestFile, exposure.CertFile, exposure.KeyFile, exposure.TokenFile} {
			if err := os.Remove(filepath.Join(dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
				h.renderExposure(w, r, http.StatusInternalServerError, "the unapplied request cannot be removed: "+err.Error())
				return
			}
		}
		h.log.Warn("exposure request never applied, replaced", "id", old.ID)
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
	files[exposure.RequestFile], _ = json.Marshal(req)
	// request.json last: the path unit fires on it. A write that fails
	// takes the files already written with it.
	var written []string
	for _, name := range []string{exposure.KeyFile, exposure.CertFile, exposure.TokenFile, exposure.RequestFile} {
		b, ok := files[name]
		if !ok {
			continue
		}
		if err := writeRequestFile(filepath.Join(dir, name), b); err != nil {
			for _, f := range written {
				os.Remove(filepath.Join(dir, f))
			}
			h.log.Error("exposure request not written", "file", name, "err", err.Error())
			h.renderExposure(w, r, http.StatusInternalServerError, "the request cannot be written: "+err.Error())
			return
		}
		written = append(written, name)
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

var writeRequestFile = writePrivate

// writePrivate writes a 0600 file aside and renames it into place: the
// path unit never reads a half-written file, and a link left at path is
// replaced, not followed.
func writePrivate(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".request-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func newRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

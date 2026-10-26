package main

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// certReloader serves the public listener's pair and picks up a new one
// when the files change, so renewing the certificate from the admin UI
// keeps the tenants' connections and sessions. The files are checked at
// most once a second; a pair that does not load (the key not yet
// written, a bad file) leaves the old one in service.
type certReloader struct {
	certFile, keyFile string
	log               *slog.Logger

	mu      sync.Mutex
	cert    *tls.Certificate
	stamp   string // mtimes and sizes of the loaded files
	failed  string // stamp of the last pair that did not load, logged once
	checked time.Time
}

func newCertReloader(certFile, keyFile string, log *slog.Logger) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, log: log}
	stamp, err := r.stampNow()
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	r.cert, r.stamp, r.checked = &cert, stamp, time.Now()
	return r, nil
}

func (r *certReloader) stampNow() (string, error) {
	s := ""
	for _, f := range []string{r.certFile, r.keyFile} {
		fi, err := os.Stat(f)
		if err != nil {
			return "", err
		}
		s += fmt.Sprintf("%d/%d;", fi.ModTime().UnixNano(), fi.Size())
	}
	return s, nil
}

func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.checked) < time.Second {
		return r.cert, nil
	}
	r.checked = time.Now()
	stamp, err := r.stampNow()
	if err != nil || stamp == r.stamp || stamp == r.failed {
		return r.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		// The error names the file and the parse step, never key bytes.
		r.failed = stamp
		r.log.Warn("public certificate not reloaded, the previous one stays in service", "cert_file", r.certFile, "err", err.Error())
		return r.cert, nil
	}
	r.cert, r.stamp, r.failed = &cert, stamp, ""
	r.log.Info("public certificate reloaded", "cert_file", r.certFile)
	return r.cert, nil
}

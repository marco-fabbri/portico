package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// UpstreamOptions describes one backend behind the proxy.
type UpstreamOptions struct {
	// Target is the backend base URL, e.g. https://10.23.210.175:9440 or
	// http://127.0.0.1:8080.
	Target *url.URL
	// HostHeader is sent as Host and X-Forwarded-Host: the public name,
	// because Prism Central and Keycloak build their URLs from it. Empty
	// keeps the inbound Host (internal pass-through).
	HostHeader string
	// TLS configures the client side for an https Target; nil for http.
	TLS *tls.Config
	// StripCookies are removed from the Cookie header before forwarding, so
	// Portico's own cookies never reach the backend.
	StripCookies []string
	Log          *slog.Logger
	// ErrorPage, when set, renders a branded page instead of the JSON
	// answer for browser navigations (GET with Accept text/html).
	ErrorPage func(w http.ResponseWriter, r *http.Request, status int, title, text, retry string)
}

// NewUpstream returns a reverse proxy that rewrites requests to the target,
// forces HTTP/1.1 (WebSocket upgrades need it), streams responses without
// buffering and answers 502 JSON when the backend is unreachable.
func NewUpstream(o UpstreamOptions) *httputil.ReverseProxy {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	strip := make(map[string]bool, len(o.StripCookies))
	for _, c := range o.StripCookies {
		strip[c] = true
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       o.TLS,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{}, // no HTTP/2
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	return &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(o.Target)
			pr.SetXForwarded()
			host := o.HostHeader
			if host == "" {
				host = pr.In.Host
			}
			pr.Out.Host = host
			pr.Out.Header.Set("X-Forwarded-Host", host)
			// The public URL is https in both exposure modes, even when
			// cloudflared delivers plain HTTP to the loopback.
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
			stripCookies(pr.Out, strip)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == context.Canceled {
				return
			}
			log.Error("upstream error", "host", o.HostHeader, "method", r.Method, "path", r.URL.Path, "class", errorClass(err))
			if o.ErrorPage != nil && r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
				o.ErrorPage(w, r, http.StatusBadGateway, "Service unavailable", "The service behind this page did not answer. Please try again in a moment.", r.URL.RequestURI())
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": "upstream unavailable"})
		},
	}
}

// stripCookies removes the named cookies and drops the header when nothing
// remains.
func stripCookies(r *http.Request, strip map[string]bool) {
	if len(strip) == 0 || r.Header.Get("Cookie") == "" {
		return
	}
	kept := make([]string, 0, 4)
	for _, c := range r.Cookies() {
		if !strip[c.Name] {
			kept = append(kept, c.Name+"="+c.Value)
		}
	}
	if len(kept) == 0 {
		r.Header.Del("Cookie")
		return
	}
	r.Header.Set("Cookie", strings.Join(kept, "; "))
}

// errorClass names the error without echoing URLs or addresses.
func errorClass(err error) string {
	var ne net.Error
	switch {
	case err == nil:
		return "none"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.As(err, &ne):
		return "network"
	default:
		return "transport"
	}
}

// PrismTLS builds the client TLS configuration for Prism Central.
func PrismTLS(targetHost, caFile string, verify bool) (*tls.Config, error) {
	var pemData []byte
	if verify && caFile != "" {
		var err error
		pemData, err = os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("prism_central.ca_file: %w", err)
		}
	}
	cfg, err := PrismTLSPEM(targetHost, pemData, verify)
	if err != nil {
		return nil, fmt.Errorf("prism_central.ca_file: %w", err)
	}
	return cfg, nil
}

// PrismTLSPEM is PrismTLS with the CA certificate given as PEM bytes
// (the settings store holds it that way); empty means the system roots.
func PrismTLSPEM(targetHost string, caPEM []byte, verify bool) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: targetHost, MinVersion: tls.VersionTLS12}
	if !verify {
		cfg.InsecureSkipVerify = true
		return cfg, nil
	}
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("no certificate found in the CA PEM")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

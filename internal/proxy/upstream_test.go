package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/branding"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type recorded struct {
	Host, Proto, Method, Path, Query, Body string
	Header                                 http.Header
	ProtoMajor                             int
}

func recordingUpstream(t *testing.T, tlsServer bool, respond func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *[]recorded) {
	t.Helper()
	var got []recorded
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, recorded{Host: r.Host, Proto: r.Proto, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(b), Header: r.Header.Clone(), ProtoMajor: r.ProtoMajor})
		if respond != nil {
			respond(w, r)
			return
		}
		w.Header().Set("X-Upstream", "yes")
		w.Write([]byte("upstream body"))
	})
	var srv *httptest.Server
	if tlsServer {
		srv = httptest.NewUnstartedServer(h)
		srv.EnableHTTP2 = true
		srv.StartTLS()
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv, &got
}

func proxyFor(t *testing.T, target string, opts UpstreamOptions) *httptest.Server {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	opts.Target = u
	if opts.HostHeader == "" {
		opts.HostHeader = "prism.test"
	}
	opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	p := httptest.NewServer(NewUpstream(opts))
	t.Cleanup(p.Close)
	return p
}

func TestUpstreamRewritesHostAndForwardedHeaders(t *testing.T) {
	up, got := recordingUpstream(t, false, nil)
	p := proxyFor(t, up.URL, UpstreamOptions{})
	resp, err := http.Get(p.URL + "/console/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("X-Upstream") != "yes" {
		t.Error("response headers not passed through")
	}
	r := (*got)[0]
	if r.Host != "prism.test" || r.Header.Get("X-Forwarded-Host") != "prism.test" || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-For") == "" {
		t.Errorf("upstream saw host %q headers %v", r.Host, r.Header)
	}
}

func TestUpstreamStripsPorticoCookies(t *testing.T) {
	up, got := recordingUpstream(t, false, nil)
	p := proxyFor(t, up.URL, UpstreamOptions{StripCookies: auth.PorticoCookies()})
	req, _ := http.NewRequest("GET", p.URL+"/console/", nil)
	// The admin UI's cookie is port-blind: a browser that holds an admin
	// session sends it to the public listener too.
	req.Header.Set("Cookie", "__Host-portico_session=a; __Host-portico_oidc=b; portico_admin=d; NTNX=c")
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	if c := (*got)[0].Header.Get("Cookie"); c != "NTNX=c" {
		t.Errorf("upstream Cookie = %q, want NTNX=c", c)
	}
	req.Header.Set("Cookie", "__Host-portico_session=a")
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	if _, present := (*got)[1].Header["Cookie"]; present {
		t.Errorf("upstream still has a Cookie header: %v", (*got)[1].Header["Cookie"])
	}
}

func TestUpstreamKeepsQueryAndBody(t *testing.T) {
	up, got := recordingUpstream(t, false, nil)
	p := proxyFor(t, up.URL, UpstreamOptions{})
	resp, err := http.Post(p.URL+"/api/nutanix/v3/groups?a=1&b=two", "application/json", strings.NewReader(`{"x":1}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err, resp)
	}
	r := (*got)[0]
	if r.Method != "POST" || r.Path != "/api/nutanix/v3/groups" || r.Query != "a=1&b=two" || r.Body != `{"x":1}` || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("upstream saw %+v", r)
	}
}

func TestUpstreamPassesSetCookieAndRedirects(t *testing.T) {
	up, _ := recordingUpstream(t, false, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "NTNX_IGW_SESSION", Value: "v", Path: "/", Secure: true, HttpOnly: true})
		http.Redirect(w, r, "https://prism.test/ui/iam/providers", http.StatusFound)
	})
	p := proxyFor(t, up.URL, UpstreamOptions{})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(p.URL + "/console/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "https://prism.test/ui/iam/providers" {
		t.Errorf("got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if len(resp.Cookies()) != 1 || resp.Cookies()[0].Name != "NTNX_IGW_SESSION" || !resp.Cookies()[0].HttpOnly {
		t.Errorf("cookies = %v", resp.Cookies())
	}
}

func TestUpstreamWebSocketUpgrade(t *testing.T) {
	up, _ := recordingUpstream(t, false, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "no upgrade", 400)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		line, _ := rw.ReadString('\n')
		rw.WriteString(line)
		rw.Flush()
	})
	p := proxyFor(t, up.URL, UpstreamOptions{})
	conn, err := net.Dial("tcp", strings.TrimPrefix(p.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET /vnc/vm/x/proxy?proxyClusterUuid=y HTTP/1.1\r\nHost: prism.test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	io.WriteString(conn, "ping\n")
	echo, err := br.ReadString('\n')
	if err != nil || echo != "ping\n" {
		t.Fatalf("echo %q err %v", echo, err)
	}
}

func TestUpstreamDown(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	p := proxyFor(t, "http://"+addr, UpstreamOptions{})
	resp, err := http.Get(p.URL + "/console/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 502 || resp.Header.Get("Content-Type") != "application/json" || strings.TrimSpace(string(b)) != `{"error":"upstream unavailable"}` {
		t.Errorf("got %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
}

func TestUpstreamUsesHTTP11(t *testing.T) {
	up, got := recordingUpstream(t, true, nil)
	p := proxyFor(t, up.URL, UpstreamOptions{TLS: &tls.Config{InsecureSkipVerify: true}})
	if _, err := http.Get(p.URL + "/console/"); err != nil {
		t.Fatal(err)
	}
	if (*got)[0].ProtoMajor != 1 {
		t.Errorf("upstream saw %s, want HTTP/1.1", (*got)[0].Proto)
	}
}

func TestPrismTLSFor(t *testing.T) {
	c, err := PrismTLSFor("10.23.210.175", SiteTrust{})
	if err != nil || !c.InsecureSkipVerify || c.ServerName != "10.23.210.175" {
		t.Errorf("verify=false: %+v %v", c, err)
	}
	up, _ := recordingUpstream(t, true, nil)
	der := up.TLS.Certificates[0].Certificate[0]
	if _, err := x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	c, err = PrismTLSFor("pc.test", SiteTrust{CAPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Verify: true})
	if err != nil || c.InsecureSkipVerify || c.RootCAs == nil {
		t.Errorf("with ca: %+v %v", c, err)
	}
}

// One site is one host: every tenant on it shares the pool, so the idle
// connections per host must not stay at net/http's default of 2 (backlog,
// final review of Milestone 6), or most requests pay a new TLS handshake.
func TestUpstreamKeepsIdleConnectionsPerHost(t *testing.T) {
	u, _ := url.Parse("https://pc.test:9440")
	tr, ok := NewUpstream(UpstreamOptions{Target: u}).Transport.(*http.Transport)
	if !ok || tr.MaxIdleConnsPerHost != tr.MaxIdleConns || tr.MaxIdleConns < 100 {
		t.Errorf("transport idle pool: per host %d, total %d", tr.MaxIdleConnsPerHost, tr.MaxIdleConns)
	}
}

func TestUpstreamKeepsInboundHostWhenUnset(t *testing.T) {
	up, got := recordingUpstream(t, false, nil)
	u, _ := url.Parse(up.URL)
	h := NewUpstream(UpstreamOptions{Target: u, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://10.23.210.190:9440/x", nil))
	if r := (*got)[0]; r.Host != "10.23.210.190:9440" || r.Header.Get("X-Forwarded-Host") != "10.23.210.190:9440" {
		t.Errorf("upstream saw host %q forwarded-host %q", r.Host, r.Header.Get("X-Forwarded-Host"))
	}
}

// Milestone 2c: a navigation to a dead upstream gets the branded page;
// everything else keeps the JSON answer of Milestone 1.
func TestUpstreamErrorPageForNavigations(t *testing.T) {
	up, _ := recordingUpstream(t, false, nil)
	up.Close()
	p := proxyFor(t, up.URL, UpstreamOptions{ErrorPage: func(w http.ResponseWriter, r *http.Request, status int, title, text, retry string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		w.Write([]byte("<html>BRANDED " + title + "</html>"))
	}})
	req, _ := http.NewRequest("GET", p.URL+"/console/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || !strings.Contains(string(b), "BRANDED") {
		t.Fatalf("navigation: %d %q", resp.StatusCode, b)
	}
	req, _ = http.NewRequest("POST", p.URL+"/api/nutanix/v3/vms/list", nil)
	req.Header.Set("Accept", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || strings.TrimSpace(string(b)) != `{"error":"upstream unavailable"}` {
		t.Fatalf("api call: %d %q", resp.StatusCode, b)
	}
}

// A navigation to "//evil.example/x" must not get a "Try again" link to
// evil.example from the branded 502 page.
func TestUpstreamErrorPageRetryOnDoubleSlashPath(t *testing.T) {
	up, _ := recordingUpstream(t, false, nil)
	up.Close()
	pages := branding.NewPages(func(context.Context) branding.Branding { return branding.Branding{MSPName: "ACME"} })
	p := proxyFor(t, up.URL, UpstreamOptions{ErrorPage: pages.Error})
	req, _ := http.NewRequest("GET", p.URL+"//evil.example/x", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || !strings.Contains(string(b), `href="/"`) || strings.Contains(string(b), "evil.example") {
		t.Fatalf("got %d %q", resp.StatusCode, b)
	}
}

// A backend cannot plant Portico's own cookies in the browser: a
// Set-Cookie carrying a stripped name or a stash name is dropped, the
// backend's other cookies pass.
func TestUpstreamSetCookieWithAPorticoNameIsDropped(t *testing.T) {
	up, _ := recordingUpstream(t, false, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "x", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: stashPrefix + "site-1", Value: "y", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "NTNX_IGW_SESSION", Value: "z", Path: "/"})
		w.WriteHeader(200)
	})
	p := proxyFor(t, up.URL, UpstreamOptions{StripCookies: auth.PorticoCookies()})
	resp, err := http.Get(p.URL + "/console/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cs := resp.Cookies()
	if len(cs) != 1 || cs[0].Name != "NTNX_IGW_SESSION" {
		t.Fatalf("cookies = %v (Set-Cookie %q)", cs, resp.Header.Values("Set-Cookie"))
	}
}

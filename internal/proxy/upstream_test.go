package proxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	p := proxyFor(t, up.URL, UpstreamOptions{StripCookies: []string{"__Host-portico_session", "__Host-portico_oidc"}})
	req, _ := http.NewRequest("GET", p.URL+"/console/", nil)
	req.Header.Set("Cookie", "__Host-portico_session=a; __Host-portico_oidc=b; NTNX=c")
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

func TestPrismTLS(t *testing.T) {
	c, err := PrismTLS("10.23.210.175", "", false)
	if err != nil || !c.InsecureSkipVerify || c.ServerName != "10.23.210.175" {
		t.Errorf("verify=false: %+v %v", c, err)
	}
	up, _ := recordingUpstream(t, true, nil)
	der := up.TLS.Certificates[0].Certificate[0]
	if _, err := x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	c, err = PrismTLS("pc.test", caFile, true)
	if err != nil || c.InsecureSkipVerify || c.RootCAs == nil {
		t.Errorf("with ca: %+v %v", c, err)
	}
	if _, err := PrismTLS("pc.test", filepath.Join(t.TempDir(), "missing.pem"), true); err == nil {
		t.Error("missing ca file should be an error")
	}
}

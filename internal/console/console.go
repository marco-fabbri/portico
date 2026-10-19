// Package console is the wizard on the appliance's tty1 (design 4.8):
// network, break-glass password, exposure, the Keycloak administrator
// credentials shown once, and the state of the services. It runs as root
// through a getty override and talks to the host through System, so the
// screens are tested against a fake.
package console

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Version is the binary's version, set by cmd/portico.
var Version = "dev"

// AdminPort is the port of the admin listener, from the process settings.
var AdminPort = "8443"

// Interface is a physical network interface of the appliance.
type Interface struct {
	Name, MAC string
	Up        bool
}

// System is everything the wizard does to the host.
type System interface {
	Interfaces() ([]Interface, error)
	ReadKeyfile(name string) (NetConfig, bool, error)
	WriteKeyfile(name string, c NetConfig) error
	Apply(name string) error
	Health(url string) (string, error)
	Unit(name string) (string, error)
	Exec(stdin io.Reader, argv ...string) (string, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, mode os.FileMode) error
}

const clear = "\x1b[2J\x1b[H"

type session struct {
	ctx context.Context
	in  *bufio.Reader
	out io.Writer
	sys System
}

// Run shows the menu until "0" or the end of the input.
func Run(ctx context.Context, in io.Reader, out io.Writer, sys System) error {
	s := &session{ctx: ctx, in: bufio.NewReader(in), out: out, sys: sys}
	for {
		s.menu()
		choice, ok := s.line()
		if !ok {
			return nil
		}
		switch strings.TrimSpace(choice) {
		case "0":
			return nil
		case "1":
			s.network()
		case "2":
			s.adminPassword()
		case "3":
			s.exposure()
		case "4":
			s.keycloakCredentials()
		case "5", "":
		default:
			fmt.Fprintln(s.out, "unknown choice")
		}
	}
}

func (s *session) menu() {
	fmt.Fprint(s.out, clear)
	url := s.adminURL()
	if url == "" {
		fmt.Fprintf(s.out, "Portico %s%40s\n", Version, "admin UI: set the network first")
	} else {
		fmt.Fprintf(s.out, "Portico %s%40s\n", Version, url+" (admin UI)")
	}
	fmt.Fprintln(s.out, s.statusLine(url))
	fmt.Fprintln(s.out, "1) Network            3) Exposure (direct certificate or Cloudflare tunnel)")
	fmt.Fprintln(s.out, "2) Admin password     4) Keycloak administrator credentials (show once)")
	fmt.Fprintln(s.out, "5) Refresh            0) Log out")
	fmt.Fprint(s.out, "> ")
}

// adminURL is the admin listener on the first configured interface.
func (s *session) adminURL() string {
	ifaces, err := s.sys.Interfaces()
	if err != nil {
		return ""
	}
	for _, i := range ifaces {
		if c, ok, _ := s.sys.ReadKeyfile(i.Name); ok && c.Address != "" {
			return "https://" + c.Address + ":" + AdminPort + "/"
		}
	}
	return ""
}

// line reads one line; ok is false at the end of the input.
func (s *session) line() (string, bool) {
	t, err := s.in.ReadString('\n')
	if err != nil && t == "" {
		return "", false
	}
	return strings.TrimRight(t, "\r\n"), true
}

// ask prompts with the default in brackets; Enter keeps the default.
func (s *session) ask(label, def string) (string, bool) {
	if def != "" {
		fmt.Fprintf(s.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(s.out, "%s: ", label)
	}
	v, ok := s.line()
	if !ok {
		return def, false
	}
	if v = strings.TrimSpace(v); v == "" {
		return def, true
	}
	return v, true
}

// askValid repeats the prompt until check accepts the answer.
func (s *session) askValid(label, def string, check func(string) error) (string, bool) {
	for {
		v, ok := s.ask(label, def)
		if !ok {
			return v, false
		}
		if err := check(v); err != nil {
			fmt.Fprintln(s.out, "  "+err.Error())
			continue
		}
		return v, true
	}
}

func (s *session) pause() {
	fmt.Fprint(s.out, "Press Enter to continue")
	s.line()
}

// network configures every interface in turn, hostname on the first.
func (s *session) network() {
	ifaces, err := s.sys.Interfaces()
	if err != nil || len(ifaces) == 0 {
		fmt.Fprintln(s.out, "no network interface found")
		s.pause()
		return
	}
	fmt.Fprintln(s.out, "Interfaces:")
	for _, i := range ifaces {
		state := "down"
		if i.Up {
			state = "up"
		}
		fmt.Fprintf(s.out, "  %s (%s, %s)\n", i.Name, i.MAC, state)
	}
	for n, i := range ifaces {
		cur, _, _ := s.sys.ReadKeyfile(i.Name)
		c := NetConfig{}
		fmt.Fprintf(s.out, "\n%s\n", i.Name)
		var ok bool
		if n == 0 {
			if c.Hostname, ok = s.askValid("Hostname", cur.Hostname, checkHostname); !ok {
				return
			}
		}
		if c.Address, ok = s.askValid("IPv4 address", cur.Address, checkAddress); !ok {
			return
		}
		def := ""
		if cur.Prefix > 0 {
			def = strconv.Itoa(cur.Prefix)
		}
		p, ok := s.askValid("Prefix length", def, checkPrefix)
		if !ok {
			return
		}
		c.Prefix, _ = strconv.Atoi(p)
		if c.Gateway, ok = s.askValid("Gateway (empty: none)", cur.Gateway, func(v string) error { return checkGateway(c.Address, c.Prefix, v) }); !ok {
			return
		}
		d, ok := s.askValid("DNS servers, space-separated (empty: none)", strings.Join(cur.DNS, " "), checkDNS)
		if !ok {
			return
		}
		c.DNS = strings.Fields(d)
		fmt.Fprintf(s.out, "Write %s: %s/%d gateway %q dns %q and apply? [y/N]: ", i.Name, c.Address, c.Prefix, c.Gateway, strings.Join(c.DNS, " "))
		yes, ok := s.line()
		if !ok {
			return
		}
		if strings.ToLower(strings.TrimSpace(yes)) != "y" {
			fmt.Fprintln(s.out, "nothing written")
			continue
		}
		if err := s.sys.WriteKeyfile(i.Name, c); err != nil {
			fmt.Fprintln(s.out, "  "+err.Error())
			continue
		}
		if n == 0 && c.Hostname != "" {
			if _, err := s.sys.Exec(nil, "hostnamectl", "set-hostname", c.Hostname); err != nil {
				fmt.Fprintln(s.out, "  hostname: "+err.Error())
			}
		}
		if err := s.sys.Apply(i.Name); err != nil {
			fmt.Fprintln(s.out, "  apply: "+err.Error())
		} else {
			fmt.Fprintf(s.out, "%s configured\n", i.Name)
		}
	}
}

func checkHostname(v string) error {
	if v == "" {
		return nil
	}
	return Validate(NetConfig{Hostname: v, Address: "10.0.0.1", Prefix: 24})
}

func checkAddress(v string) error { return Validate(NetConfig{Address: v, Prefix: 24}) }

func checkPrefix(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("console: prefix: not a number")
	}
	return Validate(NetConfig{Address: "10.0.0.1", Prefix: n})
}

func checkGateway(addr string, prefix int, v string) error {
	return Validate(NetConfig{Address: addr, Prefix: prefix, Gateway: v})
}

func checkDNS(v string) error {
	return Validate(NetConfig{Address: "10.0.0.1", Prefix: 24, DNS: strings.Fields(v)})
}

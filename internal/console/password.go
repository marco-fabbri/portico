package console

import (
	"fmt"
	"strings"
)

const (
	configPath = "/etc/portico/portico.yaml"
	porticoBin = "/usr/local/bin/portico"
)

// adminPassword sets the break-glass password through the binary run as
// the portico user: the store and the master key must belong to it, and
// the wizard runs as root. The password travels on stdin, never on a
// command line, and is never echoed.
func (s *session) adminPassword() {
	fmt.Fprintln(s.out, "Break-glass password of the admin UI and API (user admin), at least 12 characters.")
	pw, ok := s.secret("New password")
	if !ok {
		return
	}
	if len(pw) < 12 {
		fmt.Fprintln(s.out, "  at least 12 characters")
		s.pause()
		return
	}
	again, ok := s.secret("Again")
	if !ok {
		return
	}
	if pw != again {
		fmt.Fprintln(s.out, "  the two entries do not match")
		s.pause()
		return
	}
	if _, err := s.sys.Exec(strings.NewReader(pw+"\n"), "runuser", "-u", "portico", "--", porticoBin, "admin", "set-password", "-config", configPath); err != nil {
		fmt.Fprintln(s.out, "  "+err.Error())
	} else {
		fmt.Fprintln(s.out, "password set")
	}
	s.pause()
}

// Echoer is implemented by a System whose input is a terminal: the
// wizard turns the echo off while a secret is typed. The fake of the
// tests does not implement it.
type Echoer interface {
	Echo(on bool)
}

func (s *session) echo(on bool) {
	if e, ok := s.sys.(Echoer); ok {
		e.Echo(on)
	}
}

// secret reads a line with the echo off when the input is a terminal.
func (s *session) secret(label string) (string, bool) {
	fmt.Fprintf(s.out, "%s: ", label)
	s.echo(false)
	v, ok := s.line()
	s.echo(true)
	fmt.Fprintln(s.out)
	return strings.TrimSpace(v), ok
}

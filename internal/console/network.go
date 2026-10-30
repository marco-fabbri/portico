package console

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// NetConfig is what the wizard asks for one interface. Hostname, gateway
// and DNS are optional on a second interface (the internal side of a
// two-NIC appliance has no default route of its own).
type NetConfig struct {
	Hostname string
	Address  string
	Prefix   int
	Gateway  string
	DNS      []string
}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// Validate checks a configuration field by field; the error names the
// field so the wizard can ask for it again.
func Validate(c NetConfig) error {
	if c.Hostname != "" && !hostnameRe.MatchString(c.Hostname) {
		return errors.New("console: hostname: letters, digits and dashes only, 1 to 63 characters")
	}
	ip := net.ParseIP(c.Address)
	if ip == nil || ip.To4() == nil {
		return errors.New("console: address: not an IPv4 address")
	}
	if c.Prefix < 1 || c.Prefix > 32 {
		return errors.New("console: prefix: 1 to 32")
	}
	if c.Gateway != "" {
		gw := net.ParseIP(c.Gateway)
		if gw == nil || gw.To4() == nil {
			return errors.New("console: gateway: not an IPv4 address")
		}
		if gw.Equal(ip) {
			return errors.New("console: gateway: equal to the address")
		}
		mask := net.CIDRMask(c.Prefix, 32)
		if !ip.Mask(mask).Equal(gw.Mask(mask)) {
			return fmt.Errorf("console: gateway: not in %s/%d", ip.Mask(mask), c.Prefix)
		}
	}
	for _, d := range c.DNS {
		if p := net.ParseIP(d); p == nil || p.To4() == nil {
			return fmt.Errorf("console: dns: %q is not an IPv4 address", d)
		}
	}
	return nil
}

// Keyfile renders a NetworkManager keyfile for the interface: manual
// IPv4, IPv6 off (cloud-init writes the same shape).
func Keyfile(name string, c NetConfig) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[connection]\nid=%s\ntype=ethernet\ninterface-name=%s\nautoconnect=true\n\n", name, name)
	sb.WriteString("[ipv4]\nmethod=manual\n")
	if c.Gateway != "" {
		fmt.Fprintf(&sb, "address1=%s/%d,%s\n", c.Address, c.Prefix, c.Gateway)
	} else {
		fmt.Fprintf(&sb, "address1=%s/%d\n", c.Address, c.Prefix)
	}
	if len(c.DNS) > 0 {
		fmt.Fprintf(&sb, "dns=%s;\n", strings.Join(c.DNS, ";"))
	}
	sb.WriteString("\n[ipv6]\nmethod=disabled\n")
	return sb.String()
}

// ParseKeyfile reads the address, prefix, gateway and DNS back from a
// keyfile written by Keyfile, so a re-run offers the current values. A
// keyfile that is not manual IPv4 is an error: the wizard then starts
// from empty values.
func ParseKeyfile(text string) (NetConfig, error) {
	var c NetConfig
	manual := false
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "method":
			manual = manual || v == "manual"
		case "address1":
			addr, gw, _ := strings.Cut(v, ",")
			ip, prefix, ok := strings.Cut(addr, "/")
			if !ok {
				return c, errors.New("console: keyfile: address1 without a prefix")
			}
			n, err := strconv.Atoi(prefix)
			if err != nil {
				return c, errors.New("console: keyfile: bad prefix")
			}
			c.Address, c.Prefix, c.Gateway = ip, n, gw
		case "dns":
			for _, d := range strings.Split(v, ";") {
				if d != "" {
					c.DNS = append(c.DNS, d)
				}
			}
		}
	}
	if !manual || c.Address == "" {
		return c, errors.New("console: keyfile: not a manual IPv4 connection")
	}
	return c, nil
}

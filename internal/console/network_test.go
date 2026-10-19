package console

import (
	"strings"
	"testing"
)

func TestValidateNamesTheField(t *testing.T) {
	cases := map[string]NetConfig{
		"address":  {Hostname: "portico", Address: "10.0.0.300", Prefix: 24, Gateway: "10.0.0.254"},
		"prefix":   {Hostname: "portico", Address: "10.0.0.5", Prefix: 33, Gateway: "10.0.0.254"},
		"gateway":  {Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.1.254"},
		"hostname": {Hostname: "bad host", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254"},
		"dns":      {Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254", DNS: []string{"one.one"}},
	}
	for field, c := range cases {
		if err := Validate(c); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: %v", field, err)
		}
	}
	// The address equal to the gateway is refused too (Review Focus 1).
	if err := Validate(NetConfig{Hostname: "portico", Address: "10.0.0.254", Prefix: 24, Gateway: "10.0.0.254"}); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Errorf("address = gateway: %v", err)
	}
	if err := Validate(NetConfig{Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254", DNS: []string{"1.1.1.1", "9.9.9.9"}}); err != nil {
		t.Errorf("valid config refused: %v", err)
	}
	// A second interface: no hostname, no gateway, no DNS.
	if err := Validate(NetConfig{Address: "192.168.9.5", Prefix: 24}); err != nil {
		t.Errorf("plain subnet refused: %v", err)
	}
}

func TestKeyfileIsManualIPv4WithIPv6Off(t *testing.T) {
	got := Keyfile("eth0", NetConfig{Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254", DNS: []string{"1.1.1.1", "9.9.9.9"}})
	for _, want := range []string{"[connection]\nid=eth0\ntype=ethernet\ninterface-name=eth0\nautoconnect=true\n", "[ipv4]\nmethod=manual\naddress1=10.0.0.5/24,10.0.0.254\ndns=1.1.1.1;9.9.9.9;\n", "[ipv6]\nmethod=disabled\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("keyfile lacks %q:\n%s", want, got)
		}
	}
	// A second interface without a gateway is a plain subnet (the internal side).
	if got := Keyfile("eth1", NetConfig{Address: "192.168.9.5", Prefix: 24}); !strings.Contains(got, "address1=192.168.9.5/24\n") || strings.Contains(got, "dns=") {
		t.Fatalf("second interface:\n%s", got)
	}
}

func TestParseKeyfileReadsBackWhatKeyfileWrote(t *testing.T) {
	c := NetConfig{Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254", DNS: []string{"1.1.1.1", "9.9.9.9"}}
	got, err := ParseKeyfile(Keyfile("eth0", c))
	if err != nil || got.Address != c.Address || got.Prefix != 24 || got.Gateway != c.Gateway || len(got.DNS) != 2 || got.DNS[1] != "9.9.9.9" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err := ParseKeyfile("[connection]\nid=eth0\n[ipv4]\nmethod=auto\n"); err == nil {
		t.Fatal("a DHCP keyfile parsed as a manual one")
	}
}

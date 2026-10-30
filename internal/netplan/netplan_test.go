package netplan

import (
	"errors"
	"net/netip"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }
func addr(s string) netip.Addr  { return netip.MustParseAddr(s) }

func TestNextFreeLowest(t *testing.T) {
	got, err := NextFree(pfx("10.100.0.0/16"), 24, []netip.Prefix{pfx("10.100.0.0/24"), pfx("10.100.2.0/24")})
	if err != nil || got != pfx("10.100.1.0/24") {
		t.Fatalf("NextFree = %v, %v", got, err)
	}
}

func TestNextFreeSkipsException(t *testing.T) {
	// An exception inside the pool occupies its slot (design 4.2).
	got, err := NextFree(pfx("10.100.0.0/16"), 24, []netip.Prefix{pfx("10.100.0.0/24"), pfx("192.168.5.0/24")})
	if err != nil || got != pfx("10.100.1.0/24") {
		t.Fatalf("NextFree = %v, %v", got, err)
	}
}

func TestNextFreeFull(t *testing.T) {
	if _, err := NextFree(pfx("10.100.0.0/24"), 24, []netip.Prefix{pfx("10.100.0.0/24")}); !errors.Is(err, ErrPoolFull) {
		t.Fatalf("err = %v, want ErrPoolFull", err)
	}
}

func TestNextFreeTransit(t *testing.T) {
	got, err := NextFree(pfx("198.51.100.0/24"), TransitBits, []netip.Prefix{pfx("198.51.100.0/28")})
	if err != nil || got != pfx("198.51.100.16/28") {
		t.Fatalf("NextFree = %v, %v", got, err)
	}
}

func TestNextFreeUnalignedTaken(t *testing.T) {
	// A larger taken prefix covers every slot inside it.
	got, err := NextFree(pfx("198.51.100.0/24"), TransitBits, []netip.Prefix{pfx("198.51.100.0/27")})
	if err != nil || got != pfx("198.51.100.32/28") {
		t.Fatalf("NextFree = %v, %v", got, err)
	}
}

func TestTransitRange(t *testing.T) {
	gw, first, last := TransitRange(pfx("198.51.100.16/28"))
	if gw != addr("198.51.100.17") || first != addr("198.51.100.18") || last != addr("198.51.100.30") {
		t.Fatalf("TransitRange = %v %v %v", gw, first, last)
	}
}

func TestTenantRange(t *testing.T) {
	gw, first, last := TenantRange(pfx("10.100.11.0/24"))
	if gw != addr("10.100.11.1") || first != addr("10.100.11.10") || last != addr("10.100.11.250") {
		t.Fatalf("TenantRange = %v %v %v", gw, first, last)
	}
}

func TestCheckPool(t *testing.T) {
	if err := CheckPool(pfx("10.100.0.0/16")); err != nil {
		t.Fatalf("10.100.0.0/16: %v", err)
	}
	for _, bad := range []string{"8.8.0.0/16", "10.0.0.0/7", "10.100.0.0/21", "fd00::/64"} {
		if err := CheckPool(pfx(bad)); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestCheckException(t *testing.T) {
	others := []netip.Prefix{pfx("10.100.1.0/24")}
	if err := CheckException(pfx("192.168.5.0/24"), others); err != nil {
		t.Fatalf("192.168.5.0/24: %v", err)
	}
	for _, bad := range []string{"192.168.5.0/23", "10.100.1.0/24", "8.8.8.0/24"} {
		if err := CheckException(pfx(bad), others); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

// Package netplan is the address plan of tenant ready networks (tenant
// networking design 4.2): one /24 per tenant from the MSP's pool, the
// same on every site, and one /28 per tenant and site from the routed
// prefix of the site's transit VPC.
package netplan

import (
	"errors"
	"fmt"
	"net/netip"
)

// TransitBits is the size of a tenant's slice of the routed prefix: its
// VPC's SNAT address and up to MaxAllowance floating IPs.
const TransitBits = 28

// MaxAllowance is the most floating IPs a tenant can have on a site: a
// /28 pool holds 13 addresses, one of them the VPC's SNAT address.
const MaxAllowance = 12

// ErrPoolFull is NextFree finding no free slot.
var ErrPoolFull = errors.New("no free address range left in the pool")

func toU32(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func fromU32(n uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

// NextFree returns the lowest /bits inside pool that overlaps no prefix
// of taken.
func NextFree(pool netip.Prefix, bits int, taken []netip.Prefix) (netip.Prefix, error) {
	pool = pool.Masked()
	if !pool.Addr().Is4() || bits < pool.Bits() || bits > 32 {
		return netip.Prefix{}, fmt.Errorf("cannot cut /%d ranges from %s", bits, pool)
	}
	start := toU32(pool.Addr())
	step := uint64(1) << (32 - bits)
	end := uint64(start) + (uint64(1) << (32 - pool.Bits()))
	for n := uint64(start); n < end; n += step {
		cand := netip.PrefixFrom(fromU32(uint32(n)), bits)
		free := true
		for _, t := range taken {
			if t.IsValid() && t.Overlaps(cand) {
				free = false
				break
			}
		}
		if free {
			return cand, nil
		}
	}
	return netip.Prefix{}, ErrPoolFull
}

// TenantRange is a tenant /24's gateway and DHCP pool: .1, .10 to .250.
func TenantRange(p netip.Prefix) (gw, first, last netip.Addr) {
	base := toU32(p.Masked().Addr())
	return fromU32(base + 1), fromU32(base + 10), fromU32(base + 250)
}

// TransitRange is a tenant's slice of the routed prefix: the gateway is the
// first address after the network one, the pool runs from the next one to
// the address before broadcast (measured on 198.51.100.16/28).
func TransitRange(p netip.Prefix) (gw, first, last netip.Addr) {
	base := toU32(p.Masked().Addr())
	size := uint32(1) << (32 - p.Bits())
	return fromU32(base + 1), fromU32(base + 2), fromU32(base + size - 2)
}

// CheckPool accepts the MSP's tenant address pool: a private IPv4 prefix
// between /8 and /20.
func CheckPool(p netip.Prefix) error {
	if !p.Addr().Is4() || !p.Addr().IsPrivate() {
		return fmt.Errorf("%s is not a private IPv4 prefix", p)
	}
	if p.Bits() < 8 || p.Bits() > 20 {
		return fmt.Errorf("%s: the pool must be between /8 and /20", p)
	}
	if !p.Masked().Addr().IsPrivate() {
		return fmt.Errorf("%s is not a private IPv4 prefix", p)
	}
	return nil
}

// CheckException accepts a tenant subnet exception: a private IPv4 /24
// that overlaps no other tenant's subnet nor a site's external network.
func CheckException(p netip.Prefix, others []netip.Prefix) error {
	if !p.Addr().Is4() || !p.Addr().IsPrivate() || p.Bits() != 24 {
		return fmt.Errorf("%s is not a private IPv4 /24", p)
	}
	for _, o := range others {
		if o.IsValid() && o.Overlaps(p) {
			return fmt.Errorf("%s overlaps %s", p, o)
		}
	}
	return nil
}

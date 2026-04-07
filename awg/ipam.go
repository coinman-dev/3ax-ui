package awg

import "github.com/coinman-dev/3ax-ui/v2/shared/ipam"

// AllocateIPv4 finds the next free IPv4 address in the given CIDR pool.
// Returns address with /32 mask, e.g. "10.66.66.2/32".
func AllocateIPv4(pool string, serverAddr string, usedIPs []string) (string, error) {
	return ipam.AllocateIPv4(pool, serverAddr, usedIPs)
}

// AllocateIPv6 finds the next free IPv6 address in the given CIDR pool.
// Returns address with /128 mask, e.g. "2a01:xxx::2/128".
func AllocateIPv6(pool string, serverAddr string, usedIPs []string) (string, error) {
	return ipam.AllocateIPv6(pool, serverAddr, usedIPs)
}

// stripMask removes the CIDR mask from an address string.
func stripMask(addr string) string {
	return ipam.StripMask(addr)
}

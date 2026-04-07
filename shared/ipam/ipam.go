package ipam

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"net"
	"strings"
)

// AllocateIPv4 finds the next free IPv4 address in the given CIDR pool.
// serverAddr is the server's own address (to skip), usedIPs are already allocated addresses.
// Returns address with /32 mask, e.g. "10.66.66.2/32".
func AllocateIPv4(pool string, serverAddr string, usedIPs []string) (string, error) {
	_, ipNet, err := net.ParseCIDR(pool)
	if err != nil {
		return "", fmt.Errorf("invalid IPv4 pool %q: %w", pool, err)
	}

	used := make(map[string]bool)
	if serverAddr != "" {
		sip := StripMask(serverAddr)
		used[sip] = true
	}
	for _, a := range usedIPs {
		used[StripMask(a)] = true
	}

	ip := ipNet.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("pool %q is not IPv4", pool)
	}

	ipInt := binary.BigEndian.Uint32(ip)
	ones, bits := ipNet.Mask.Size()
	hostCount := uint32(1) << uint(bits-ones)

	// Start from .2 (skip .0 network and .1 typically server)
	for i := uint32(2); i < hostCount-1; i++ {
		candidate := make(net.IP, 4)
		binary.BigEndian.PutUint32(candidate, ipInt+i)
		if !used[candidate.String()] {
			return candidate.String() + "/32", nil
		}
	}

	return "", fmt.Errorf("no free IPv4 addresses in pool %s", pool)
}

// AllocateIPv6 finds the next free IPv6 address in the given CIDR pool.
// Returns address with /128 mask, e.g. "2a01:xxx::2/128".
func AllocateIPv6(pool string, serverAddr string, usedIPs []string) (string, error) {
	_, ipNet, err := net.ParseCIDR(pool)
	if err != nil {
		return "", fmt.Errorf("invalid IPv6 pool %q: %w", pool, err)
	}

	used := make(map[string]bool)
	if serverAddr != "" {
		sip := StripMask(serverAddr)
		parsed := net.ParseIP(sip)
		if parsed != nil {
			used[parsed.String()] = true
		}
	}
	for _, a := range usedIPs {
		stripped := StripMask(a)
		parsed := net.ParseIP(stripped)
		if parsed != nil {
			used[parsed.String()] = true
		}
	}

	baseIP := ipNet.IP.To16()
	if baseIP == nil {
		return "", fmt.Errorf("pool %q is not valid IP", pool)
	}

	ones, _ := ipNet.Mask.Size()
	hostBits := 128 - ones
	maxHosts := int64(1) << hostBits
	if maxHosts > 65536 {
		maxHosts = 65536
	}

	base := new(big.Int).SetBytes(baseIP)

	// Start from ::2
	for i := int64(2); i < maxHosts; i++ {
		candidate := new(big.Int).Add(base, big.NewInt(i))
		candidateBytes := candidate.Bytes()
		ip := make(net.IP, 16)
		copy(ip[16-len(candidateBytes):], candidateBytes)

		if !used[ip.String()] {
			return ip.String() + "/128", nil
		}
	}

	return "", fmt.Errorf("no free IPv6 addresses in pool %s", pool)
}

// StripMask removes the CIDR mask from an address string.
func StripMask(addr string) string {
	if idx := strings.IndexByte(addr, '/'); idx >= 0 {
		return addr[:idx]
	}
	return addr
}

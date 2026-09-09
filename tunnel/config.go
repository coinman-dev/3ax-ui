package tunnel

import (
	"fmt"
	"net"
	"strings"

	"github.com/coinman-dev/3ax-ui/v2/shared/ipam"
	"github.com/coinman-dev/3ax-ui/v2/shared/portfwd"
	"github.com/coinman-dev/3ax-ui/v2/shared/tproxy"
)

// DetectDefaultInterface returns the first non-loopback, non-tunnel, UP interface
// that has a routable IP address. Falls back to "eth0" only if nothing is found.
func DetectDefaultInterface() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "eth0"
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		if strings.HasPrefix(iface.Name, "awg") || strings.HasPrefix(iface.Name, "wg") ||
			strings.HasPrefix(iface.Name, "docker") || strings.HasPrefix(iface.Name, "br-") ||
			strings.HasPrefix(iface.Name, "veth") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLinkLocalUnicast() && ipNet.IP.To4() != nil {
				return iface.Name
			}
		}
	}
	return "eth0"
}

// writeObfuscation writes the AmneziaWG obfuscation parameters. Everything a
// generation adds on top of 1.x is emitted only when set, so a 1.x server
// (S3=S4=0, no I values, no 3.0 fields) still produces classic output, a 2.0
// server adds the extra padding, header ranges and signature packets, and a 3.0
// server adds header protection and the randomised timers on top of that.
//
// The 2.0 set has to match on both ends of a tunnel and so does 3.0's
// HeaderProtectionKey; the remaining 3.0 parameters are one-sided, but they are
// written to both configs anyway so a client obfuscates the way the server
// does. No-op for flavours without obfuscation.
func writeObfuscation(b *strings.Builder, k Kind, server *Server) {
	if !k.Obfuscation {
		return
	}
	fmt.Fprintf(b, "Jc = %d\n", server.Jc)
	fmt.Fprintf(b, "Jmin = %d\n", server.Jmin)
	fmt.Fprintf(b, "Jmax = %d\n", server.Jmax)
	fmt.Fprintf(b, "S1 = %d\n", server.S1)
	fmt.Fprintf(b, "S2 = %d\n", server.S2)
	if server.S3 > 0 {
		fmt.Fprintf(b, "S3 = %d\n", server.S3)
	}
	if server.S4 > 0 {
		fmt.Fprintf(b, "S4 = %d\n", server.S4)
	}
	fmt.Fprintf(b, "H1 = %s\n", hOrDefault(server.H1, "1"))
	fmt.Fprintf(b, "H2 = %s\n", hOrDefault(server.H2, "2"))
	fmt.Fprintf(b, "H3 = %s\n", hOrDefault(server.H3, "3"))
	fmt.Fprintf(b, "H4 = %s\n", hOrDefault(server.H4, "4"))
	for i, v := range []string{server.I1, server.I2, server.I3, server.I4, server.I5} {
		if v != "" {
			fmt.Fprintf(b, "I%d = %s\n", i+1, v)
		}
	}
	// Only when the kernel will take them. They are kept in the record either
	// way, so upgrading the module brings them back without the operator
	// having to set anything up again — but writing keys this module refuses
	// costs the whole interface, not just the feature.
	if SupportsV3(k) {
		writeObfuscation30(b, server)
	}
}

// writeObfuscation30 appends the AmneziaWG 3.0 parameters. Each one is skipped
// when unset: the kernel then keeps its own default, which is what every server
// configured before 3.0 existed relies on.
func writeObfuscation30(b *strings.Builder, server *Server) {
	if key := strings.TrimSpace(server.HeaderProtectionKey); key != "" {
		fmt.Fprintf(b, "HeaderProtectionKey = %s\n", key)
	}
	for _, p := range []struct{ key, value string }{
		{"ContentPaddingAddition", server.ContentPaddingAddition},
		{"RekeyAfterTime", server.RekeyAfterTime},
		{"RekeyTimeout", server.RekeyTimeout},
		{"RejectAfterTime", server.RejectAfterTime},
		{"KeepaliveTimeout", server.KeepaliveTimeout},
		{"MaxHandshakeAttempts", server.MaxHandshakeAttempts},
	} {
		if v := strings.TrimSpace(p.value); v != "" {
			fmt.Fprintf(b, "%s = %s\n", p.key, v)
		}
	}
	// Both default to off in the kernel, so "off" is never worth writing.
	if server.RandomTrailers {
		b.WriteString("RandomTrailers = on\n")
	}
	if server.DisableCookies {
		b.WriteString("DisableCookies = on\n")
	}
}

// hOrDefault returns def when v is blank, guarding against an empty H value
// (which would emit an invalid "H1 = " line) on legacy/partial records.
func hOrDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// ifaceName returns the tunnel interface name, falling back to the flavour's
// default when the record has none.
func ifaceName(k Kind, server *Server) string {
	if server.InterfaceName != "" {
		return server.InterfaceName
	}
	return k.DefaultIface
}

// externalIface returns the interface used for IPv4 NAT.
func externalIface(server *Server) string {
	if server.ExternalInterface != "" {
		return server.ExternalInterface
	}
	return DetectDefaultInterface()
}

// ipv6Iface returns the external interface for IPv6 operations, falling back to
// the IPv4 external interface when not set separately.
func ipv6Iface(server *Server) string {
	if server.IPv6ExternalInterface != "" {
		return server.IPv6ExternalInterface
	}
	if server.ExternalInterface != "" {
		return server.ExternalInterface
	}
	return DetectDefaultInterface()
}

// GenerateServerConfig builds the <interface>.conf content from server settings
// and clients.
func GenerateServerConfig(k Kind, server *Server, clients []Client) string {
	var b strings.Builder

	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", server.PrivateKey)

	addresses := []string{server.IPv4Address}
	if server.IPv6Enabled && server.IPv6Address != "" {
		addresses = append(addresses, server.IPv6Address)
	}
	fmt.Fprintf(&b, "Address = %s\n", strings.Join(addresses, ", "))

	fmt.Fprintf(&b, "ListenPort = %d\n", server.ListenPort)

	if server.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", server.MTU)
	}

	writeObfuscation(&b, k, server)

	postUp := server.PostUp
	if postUp == "" {
		postUp = GenerateDefaultPostUp(k, server, clients)
	}
	postDown := server.PostDown
	if postDown == "" {
		postDown = GenerateDefaultPostDown(k, server, clients)
	}
	if postUp != "" {
		fmt.Fprintf(&b, "PostUp = %s\n", postUp)
	}
	if postDown != "" {
		fmt.Fprintf(&b, "PostDown = %s\n", postDown)
	}

	for _, c := range clients {
		if !c.Enable {
			continue
		}
		b.WriteString("\n[Peer]\n")
		fmt.Fprintf(&b, "# %s\n", c.Name)
		fmt.Fprintf(&b, "PublicKey = %s\n", c.PublicKey)
		if c.PresharedKey != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", c.PresharedKey)
		}
		fmt.Fprintf(&b, "AllowedIPs = %s\n", c.AllowedIPs)
	}

	return b.String()
}

// GenerateClientConfig builds a client .conf file content.
func GenerateClientConfig(k Kind, server *Server, client Client) string {
	var b strings.Builder

	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", client.PrivateKey)

	addresses := []string{client.IPv4Address}
	if server.IPv6Enabled && client.IPv6Address != "" {
		addresses = append(addresses, client.IPv6Address)
	}
	fmt.Fprintf(&b, "Address = %s\n", strings.Join(addresses, ", "))

	// The client DNS line is composed from the per-family fields; the IPv6 one
	// is included only when IPv6 is enabled on the server.
	var dnsParts []string
	if server.DnsIpv4 != "" {
		dnsParts = append(dnsParts, server.DnsIpv4)
	}
	if server.IPv6Enabled && server.DnsIpv6 != "" {
		dnsParts = append(dnsParts, server.DnsIpv6)
	}
	if len(dnsParts) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(dnsParts, ", "))
	}

	if server.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", server.MTU)
	}

	// The client must carry the same obfuscation parameters as the server.
	writeObfuscation(&b, k, server)

	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", server.PublicKey)
	if client.PresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", client.PresharedKey)
	}

	endpoint := server.Endpoint
	if endpoint != "" {
		if !strings.Contains(endpoint, ":") {
			endpoint = fmt.Sprintf("%s:%d", endpoint, server.ListenPort)
		}
		fmt.Fprintf(&b, "Endpoint = %s\n", endpoint)
	}

	allowedIPs := client.ClientAllowedIPs
	if allowedIPs == "" {
		allowedIPs = "0.0.0.0/0, ::/0"
	}
	fmt.Fprintf(&b, "AllowedIPs = %s\n", allowedIPs)

	if client.PersistentKeepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", client.PersistentKeepalive)
	}

	return b.String()
}

// tproxyParams describes this tunnel's TPROXY wiring for shared/tproxy.
func tproxyParams(k Kind, server *Server, name string) tproxy.Params {
	return tproxy.Params{
		Interface:   name,
		Port:        server.XrayTproxyPort,
		Fwmark:      k.TproxyFwmark,
		Table:       k.TproxyTable,
		DefaultPort: k.TproxyPort,
		IPv6:        server.IPv6Enabled,
	}
}

// GenerateDefaultPostUp creates the default iptables + NDP proxy rules.
func GenerateDefaultPostUp(k Kind, server *Server, clients []Client) string {
	iface := externalIface(server)
	name := ifaceName(k, server)

	parts := []string{
		fmt.Sprintf("iptables -A FORWARD -i %s -j ACCEPT", name),
		fmt.Sprintf("iptables -A FORWARD -o %s -j ACCEPT", name),
	}
	if !server.RouteViaXray {
		// In direct mode, NAT tunnel traffic to the external interface. Under
		// RouteViaXray the TPROXY rules below capture ingress before it ever
		// reaches POSTROUTING, so MASQUERADE is unwanted.
		parts = append([]string{
			fmt.Sprintf("iptables -t nat -A POSTROUTING -s %s -o %s -j MASQUERADE", server.IPv4Pool, iface),
		}, parts...)
	}

	if server.IPv6Enabled {
		iface6 := ipv6Iface(server)
		parts = append(parts,
			fmt.Sprintf("ip6tables -A FORWARD -i %s -j ACCEPT", name),
			fmt.Sprintf("ip6tables -A FORWARD -o %s -j ACCEPT", name),
			fmt.Sprintf("ip6tables -A FORWARD -i %s -o %s -j ACCEPT", iface6, name),
			"sysctl -w net.ipv6.conf.all.forwarding=1",
			fmt.Sprintf("sysctl -w net.ipv6.conf.%s.proxy_ndp=1", iface6),
		)
		for _, c := range clients {
			if c.Enable && c.IPv6Address != "" {
				parts = append(parts,
					// replace, not add: an entry left over from a bring-up
					// that failed later would make this one fail with "File
					// exists", and PostUp stops at the first error.
					fmt.Sprintf("ip -6 neigh replace proxy %s dev %s", ipam.StripMask(c.IPv6Address), iface6),
				)
			}
		}
	}
	parts = append(parts, "sysctl -w net.ipv4.ip_forward=1")

	// Per-client port forwarding (DNAT). Only enabled clients get rules.
	for _, c := range clients {
		if !c.Enable || c.ForwardedPorts == "" {
			continue
		}
		specs := portfwd.Parse(c.ForwardedPorts)
		rules := portfwd.Rules(iface, name, c.IPv4Address, c.UUID, specs)
		parts = append(parts, portfwd.PostUpLines(rules)...)
	}

	if server.RouteViaXray {
		parts = append(parts, tproxy.PostUpLines(tproxyParams(k, server, name))...)
	}

	return strings.Join(parts, "; ")
}

// GenerateDefaultPostDown creates cleanup rules matching PostUp.
func GenerateDefaultPostDown(k Kind, server *Server, clients []Client) string {
	iface := externalIface(server)
	name := ifaceName(k, server)

	parts := []string{
		fmt.Sprintf("iptables -D FORWARD -i %s -j ACCEPT", name),
		fmt.Sprintf("iptables -D FORWARD -o %s -j ACCEPT", name),
	}
	if !server.RouteViaXray {
		parts = append([]string{
			fmt.Sprintf("iptables -t nat -D POSTROUTING -s %s -o %s -j MASQUERADE", server.IPv4Pool, iface),
		}, parts...)
	}

	if server.IPv6Enabled {
		iface6 := ipv6Iface(server)
		parts = append(parts,
			fmt.Sprintf("ip6tables -D FORWARD -i %s -j ACCEPT", name),
			fmt.Sprintf("ip6tables -D FORWARD -o %s -j ACCEPT", name),
			fmt.Sprintf("ip6tables -D FORWARD -i %s -o %s -j ACCEPT", iface6, name),
		)
		for _, c := range clients {
			if c.Enable && c.IPv6Address != "" {
				parts = append(parts,
					fmt.Sprintf("ip -6 neigh del proxy %s dev %s", ipam.StripMask(c.IPv6Address), iface6),
				)
			}
		}
	}

	for _, c := range clients {
		if !c.Enable || c.ForwardedPorts == "" {
			continue
		}
		specs := portfwd.Parse(c.ForwardedPorts)
		rules := portfwd.Rules(iface, name, c.IPv4Address, c.UUID, specs)
		parts = append(parts, portfwd.PostDownLines(rules)...)
	}

	if server.RouteViaXray {
		parts = append(parts, tproxy.PostDownLines(tproxyParams(k, server, name))...)
	}

	return strings.Join(parts, "; ")
}

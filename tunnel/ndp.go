package tunnel

import (
	"github.com/coinman-dev/3ax-ui/v2/shared/ndppd"
)

// Each flavour owns its own marked block of /etc/ndppd.conf, so applying one
// never disturbs the other (or any rule the operator wrote by hand).
var ndppdSections = map[string]*ndppd.Section{
	AWG.Name: ndppd.NewSection(AWG.NdppdSection),
	WG.Name:  ndppd.NewSection(WG.NdppdSection),
}

func section(k Kind) *ndppd.Section {
	if s, ok := ndppdSections[k.Name]; ok {
		return s
	}
	s := ndppd.NewSection(k.NdppdSection)
	ndppdSections[k.Name] = s
	return s
}

// ApplyNdppdConfig updates only this flavour's section of ndppd.conf and
// restarts the daemon.
func ApplyNdppdConfig(k Kind, externalIface, tunnelIface, ipv6Pool string) error {
	return section(k).Apply(externalIface, tunnelIface, ipv6Pool)
}

// StopNdppd removes this flavour's section; the daemon keeps running if the
// other flavour still needs it.
func StopNdppd(k Kind) { section(k).Stop() }

// AddProxyNDP adds a single IPv6 NDP proxy entry (fallback without ndppd).
func AddProxyNDP(ipv6 string, externalIface string) error {
	return ndppd.AddProxy(ipv6, externalIface)
}

// RemoveProxyNDP removes a single IPv6 NDP proxy entry.
func RemoveProxyNDP(ipv6 string, externalIface string) error {
	return ndppd.RemoveProxy(ipv6, externalIface)
}

// IsNdppdInstalled reports whether ndppd is available on the system.
func IsNdppdInstalled() bool { return ndppd.IsInstalled() }

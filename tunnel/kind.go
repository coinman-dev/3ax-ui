// Package tunnel implements the WireGuard-family tunnels the panel manages:
// AmneziaWG and native WireGuard.
//
// The two are the same protocol family with the same data model — the panel's
// client records are field-for-field identical and an AmneziaWG server is a
// WireGuard server plus obfuscation parameters. Everything that genuinely
// differs (tool names, config directory, kernel module, the fwmark/table pair
// that keeps their TPROXY setups apart, and whether obfuscation applies) is
// described by a Kind, so there is one implementation instead of two copies.
package tunnel

import "github.com/coinman-dev/3ax-ui/v2/database/model"

// Kind describes one tunnel flavour.
type Kind struct {
	Name  string // short id used in logs and config paths: "awg" / "wg"
	Title string // human name: "AmneziaWG" / "WireGuard"

	Tool  string // wg-style CLI: awg / wg
	Quick string // wg-quick-style CLI: awg-quick / wg-quick

	ConfigDir    string // where <interface>.conf lives
	KernelModule string // module name under /sys/module, for version reporting
	DefaultIface string // interface name used when the server record has none

	NdppdSection string // marker name of this tunnel's block in /etc/ndppd.conf

	// TPROXY namespacing. AmneziaWG and WireGuard can both route through Xray
	// at the same time, so each needs its own fwmark, routing table and default
	// dokodemo-door port — overlapping values would make them fight over the
	// same policy route.
	TproxyFwmark string
	TproxyTable  string
	TproxyPort   int

	// Obfuscation reports whether this flavour carries the Amnezia parameters
	// (Jc/Jmin/Jmax/S1-S4/H1-H4/I1) in its configs.
	Obfuscation bool

	// Defaults applied when a server record is created or reset. The two
	// flavours must not share a subnet or a listen port — they can run side by
	// side on the same host.
	DefaultIPv4Address string
	DefaultIPv4Pool    string
	// LegacyListenPort is the fixed port older versions of the panel wrote into
	// fresh records; hitting it means "never chosen deliberately, pick a random
	// one instead".
	LegacyListenPort int
	// InboundProtocol is the protocol string of the panel inbound that
	// represents this tunnel in the inbounds list.
	InboundProtocol model.Protocol
}

var (
	// AWG is AmneziaWG: WireGuard plus obfuscation, its own tools and module.
	AWG = Kind{
		Name:         "awg",
		Title:        "AmneziaWG",
		Tool:         "awg",
		Quick:        "awg-quick",
		ConfigDir:    "/etc/amnezia/amneziawg",
		KernelModule: "amneziawg",
		DefaultIface: "awg0",
		NdppdSection: "AWG",
		TproxyFwmark: "0x1",
		TproxyTable:  "100",
		TproxyPort:   12345,
		Obfuscation:  true,

		DefaultIPv4Address: "10.66.66.1/24",
		DefaultIPv4Pool:    "10.66.66.0/24",
		LegacyListenPort:   51820,
		InboundProtocol:    model.AmneziaWG,
	}

	// WG is native WireGuard.
	WG = Kind{
		Name:         "wg",
		Title:        "WireGuard",
		Tool:         "wg",
		Quick:        "wg-quick",
		ConfigDir:    "/etc/wireguard",
		KernelModule: "wireguard",
		DefaultIface: "wg0",
		NdppdSection: "WG",
		TproxyFwmark: "0x2",
		TproxyTable:  "101",
		TproxyPort:   12346,
		Obfuscation:  false,

		DefaultIPv4Address: "10.77.77.1/24",
		DefaultIPv4Pool:    "10.77.77.0/24",
		LegacyListenPort:   51821,
		InboundProtocol:    model.NativeWG,
	}
)

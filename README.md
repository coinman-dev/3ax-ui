[English](/README.md) | [Русский](/README.ru_RU.md)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./media/3ax-ui-dark.png">
    <img alt="3ax-ui" src="./media/3ax-ui-light.png">
  </picture>
</p>

[![Release](https://img.shields.io/github/v/release/coinman-dev/3ax-ui.svg)](https://github.com/coinman-dev/3ax-ui/releases)
[![Build](https://img.shields.io/github/actions/workflow/status/coinman-dev/3ax-ui/release.yml.svg)](https://github.com/coinman-dev/3ax-ui/actions)
[![GO Version](https://img.shields.io/github/go-mod/go-version/coinman-dev/3ax-ui.svg)](#)
[![Downloads](https://img.shields.io/github/downloads/coinman-dev/3ax-ui/total.svg)](https://github.com/coinman-dev/3ax-ui/releases/latest)
[![License](https://img.shields.io/badge/license-GPL%20V3-blue.svg?longCache=true)](https://www.gnu.org/licenses/gpl-3.0.en.html)

**3AX-UI** is a fork of [3x-ui](https://github.com/MHSanaei/3x-ui) with built-in support for the **AmneziaWG** protocol.

> The **A** in the name stands for **Amnezia** — the protocol that is the key difference between this panel and the original.

> [!IMPORTANT]
> This project is intended for personal use only. Please do not use it for illegal purposes.

## Quick Start

```bash
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh)
```

To install the latest pre-release version:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh) --beta
```
---

## Why this panel?

The original 3x-ui is built around the **Xray** core and supports VLESS, VMess, Trojan, Shadowsocks, and WireGuard. However, **AmneziaWG** — a modified WireGuard with traffic obfuscation — is not supported in the original.

**3AX-UI** solves this: AmneziaWG is integrated directly into the panel and managed exactly like any other protocol through the familiar inbounds interface.

---

## Key differences from 3x-ui

### 1. Full AmneziaWG support

AmneziaWG is WireGuard with added packet obfuscation. Standard WireGuard is easily detected and blocked by DPI systems (Russia, Iran, China). AmneziaWG makes traffic indistinguishable from random noise.

**What's added:**
- Dedicated AWG server settings page (network parameters, IPv4/IPv6 address pool, obfuscation parameters)
- AWG client management directly from the **Inbounds** page — just like VLESS or Trojan
- Per-client: automatic key generation (private, public, preshared), IP allocation from pool, QR code, `.conf` file download
- Traffic statistics collected every 10 seconds (upload/download per client)
- Traffic limits and expiry dates — same as all other protocols

### 2. AmneziaWG obfuscation parameters

The AWG settings page lets you configure packet obfuscation parameters:

| Parameter | Description |
|-----------|-------------|
| `Jc` | Number of junk packets before handshake |
| `Jmin` / `Jmax` | Minimum and maximum size of junk packets |
| `S1` / `S2` | Size of init/response headers |
| `H1` – `H4` | Magic headers for different packet types |

These parameters are automatically written into each client's config — no manual configuration needed.

### 3. Native IPv6 support without NAT

AWG clients can be assigned a **native public IPv6 address** from the server — without NAT66. This works via NDP proxy (ndppd or a built-in fallback using `ip -6 neigh add proxy`). Clients receive a real IPv6 address, which matters for services that require it.

#### If IPv6 doesn't work: provider-side limitations

NDP proxy may not work on a VPS for reasons outside your server's control:

**1. Hypervisor blocks NDP packets (MAC filtering)**

Many providers allow a VPS to send packets only from its own network interface MAC address. When `ndppd` forwards a Neighbor Advertisement on behalf of a client, the hypervisor treats this as IP spoofing and drops the packet. Everything looks correct inside the VPS, but client IPv6 traffic never reaches the internet.

**2. Provider assigns a "link prefix" instead of a "routed prefix"**

NDP proxy only works when the IPv6 block is **routed directly to your VPS**. Many providers connect multiple VPSes to a shared virtual network and assign addresses from a common pool — in this case, NDP proxy at the VPS level won't help.

#### What to do

Contact your provider's support. You need to find out:
- **IPv6 allocation type:** is it a fully routed /64 prefix (routed to your VM) or an address from a shared pool (link prefix)? Only a routed prefix allows NDP proxy to work.
- **Hypervisor-level NDP proxy:** does the control panel have an option to enable NDP proxy / Neighbor Discovery at the host level?
- **IP spoofing allowance:** ask them to allow NDP packet forwarding from your VPS (disable MAC filtering for your interface at the hypervisor level).

> **Message template for provider support:**
> *"I'm running a server with multiple virtual network interfaces and need to assign individual public IPv6 addresses from my /64 block to each of them using NDP proxy. Could you please confirm whether my IPv6 allocation is a fully routed /64 prefix routed to my VM directly, and whether NDP Neighbor Advertisement packets originated from my VM are allowed through the hypervisor — or if they are dropped by MAC/ARP filtering on the host node?"*

### 4. Automatic AmneziaWG installation

The install script (`install.sh`) automatically:
- Installs the AmneziaWG kernel module via PPA `ppa:amnezia/ppa`
- Installs `awg-tools` and `ndppd`
- Detects the server's external interface and configures PostUp/PostDown rules
- Sets up AWG autostart after server reboot
- Detects Secure Boot and warns about potential DKMS module issues

### 5. Configurable QR code size

The panel settings include a **QR Code Size** option:
- 300×300 px — compact
- 450×450 px — standard (default)
- 600×600 px — large

### 6. Secure subscription URL by default

On installation, the subscription URL path is automatically generated with a random 12-character suffix (e.g. `/sub-Xk92mPqLvzRt/`) instead of the default `/sub/`. This reduces the risk of accidental discovery.

---

## Server requirements

- **OS:** Ubuntu 22.04+ / Debian 11+
- **Linux kernel:** 5.6+ (for built-in WireGuard), or an installed AmneziaWG DKMS module
- **RAM:** 1024 MB or more
- **Architecture:** amd64 / arm64

> **Secure Boot:** If Secure Boot is enabled on the server, the AmneziaWG DKMS module may fail to load. The install script will warn you automatically.

---

## Installation

```bash
# Stable release
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh)

# Latest pre-release
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh) --beta

# Specific version
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh) v1.2.1
```

## Panel Update

```bash
# Stable release
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/update.sh)

# Latest pre-release
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/update.sh) --beta
```

---

## AmneziaWG quick start

1. Log into the panel → **AWG Settings**
2. Configure network parameters and obfuscation settings
3. Go to **Inbounds** → **Add Inbound**
4. Select the **amneziawg** protocol, enter a client email, and click **Create**
5. In the client table, click the QR code icon and scan it in the AmneziaVPN app

---

## Compatible AmneziaWG clients

| Client | Platform | Link |
|--------|----------|------|
| AmneziaVPN | Android, iOS, Windows, macOS, Linux | [amnezia.org](https://amnezia.org) |

> Standard WireGuard clients are **not compatible** with AmneziaWG — they do not support obfuscation parameters.

---

## Based on

3AX-UI is based on **[3x-ui](https://github.com/MHSanaei/3x-ui)** by [MHSanaei](https://github.com/MHSanaei). All original features (VLESS, VMess, Trojan, Shadowsocks, WireGuard, Xray, subscriptions, Telegram bot, etc.) are fully preserved.

## Acknowledgements

- [MHSanaei](https://github.com/MHSanaei/) — author of the original 3x-ui
- [alireza0](https://github.com/alireza0/) — author of the original x-ui
- [Iran v2ray rules](https://github.com/chocolate4u/Iran-v2ray-rules) (GPL-3.0)
- [Russia v2ray rules](https://github.com/runetfreedom/russia-v2ray-rules-dat) (GPL-3.0)

---

## License

This project is distributed under the same license as the original 3x-ui — [GNU GPL v3](LICENSE).

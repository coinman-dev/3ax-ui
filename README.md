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

### 7. Per-client port forwarding for AmneziaWG / native WireGuard

Each peer can forward arbitrary external ports straight to its tunnel IP for both **TCP and UDP** simultaneously — designed for game servers, P2P, voice apps, anything that needs an inbound port.

**Input format** (free-form, validated):
- single ports: `80, 443, 22`
- ranges with a dash: `8000-8100`
- mix freely, separated by `,` or `;`: `80, 443; 27015-27030`

**How it works.** For each enabled client with non-empty forwarded ports the panel emits `iptables` DNAT + FORWARD rules (TCP and UDP) into wg-quick's `PostUp`/`PostDown`. Updates apply **live** via `iptables -A`/`-D` without restarting the tunnel — peer sessions are not interrupted. Each rule carries a unique `3ax-fwd-<uuid>` comment so removing one client's forwards never touches another's.

The forwarded ports are visible in three places:
- the client edit form (with format hint),
- a dedicated "Mapping" column in the inbound's peer table,
- a row in the details modal directly under "Port".

### 8. SOCKS5 and HTTP proxies with full per-user infrastructure

xray-core's `mixed` (SOCKS5) and `http` inbounds now share the **same VLESS-style stack** as VLESS / VMess / Trojan / Shadowsocks:
- expandable peer table with per-client traffic, expiry, quota, IP limit, enable toggle;
- standard rich client edit modal (auto-generated 6-character username + 16-character password, regenerable);
- per-user traffic stats flow through xray's standard `user>>>EMAIL>>>traffic>>>...` keys, so the existing traffic and disable-on-quota / disable-on-expiry jobs handle MIXED/HTTP automatically;
- "Add Client" entry in the inbound action menu, just like VLESS.

The username remains editable after creation — renaming a client doesn't reset its traffic counters because the backend renames the underlying `client_traffic` row in place.

### 9. Install / update from a local git clone

Both `install.sh` and `update.sh` detect when they are being run from inside a cloned repository (file presence + a BASH_SOURCE safety check) and **build the panel binary on the spot from the local source** instead of downloading the pre-built release tarball.

```bash
git clone https://github.com/coinman-dev/3ax-ui.git
cd 3ax-ui
sudo bash install.sh
```

If Go ≥ 1.21 isn't on the host, the script downloads Go 1.26.2 from go.dev automatically. With Go ≥ 1.21 the build self-bootstraps the toolchain pinned in `go.mod`. The remote-pipe flows (`bash <(curl ...)`, `curl ... | bash`) keep the existing GitHub-release behavior — the safety check rejects them so a user happening to be inside a clone of the repo while piping the script can't accidentally hit the local-build path.

`x-ui.db` and `bin/` survive across re-installs and updates, so re-running the installer does not wipe the panel database.

### 10. Debug / diagnostic install mode

A first prompt at install time:

```
Install panel in debug / diagnostic mode (localhost only)? [y/N]
(HTTP only, listen=127.0.0.1, default port 8080, no SSL or IPv6)
```

On `y` the panel binds to `127.0.0.1`, runs over plain HTTP on the chosen port, and skips the SSL prompt, the public-IP detection, and IPv6 work. Activate non-interactively with `XUI_DEBUG_MODE=1` (and optional `XUI_DEBUG_PORT=NNNN`).

`update.sh` **doesn't ask** the question — it auto-detects whether the existing install is in debug mode (`listenIP == 127.0.0.1` and no SSL cert configured) and inherits the same setup with the existing port, so updates are non-interactive on a debug box.

VPN protocol stacks (AmneziaWG, native WireGuard, xray) install normally in debug mode — only the panel's web access is restricted to the loopback.

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

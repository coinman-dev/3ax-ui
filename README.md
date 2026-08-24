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

**3AX-UI** is a fork of [3x-ui](https://github.com/MHSanaei/3x-ui) with built-in censorship-circumvention protocols the original lacks: **AmneziaWG** (through 3.1), **native WireGuard** with native IPv6, and **MTProto** (a Telegram proxy).

> The **A** in the name stands for **Amnezia** — the protocol this fork started with and still its key difference from the original.

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

The original 3x-ui is built around the **Xray** core and supports VLESS, VMess, Trojan, Shadowsocks, and WireGuard. But the most useful DPI-circumvention tools today are missing from the original:

- **AmneziaWG** — a modified WireGuard with traffic obfuscation (every generation through 3.1);
- **native WireGuard** that hands clients a real public IPv6 address without NAT66;
- **MTProto** — a FakeTLS proxy for Telegram.

**3AX-UI** integrates all three directly into the panel: they are created and managed exactly like any other protocol through the familiar **Inbounds** page.

---

## Key differences from 3x-ui

### 1. Full AmneziaWG support (1.x, 2.0, 3.0 and 3.1)

AmneziaWG is WireGuard with added packet obfuscation. Standard WireGuard is easily detected and blocked by DPI systems (Russia, Iran, China). AmneziaWG makes traffic indistinguishable from random noise.

**What's added:**
- Dedicated AWG server settings page (network parameters, IPv4/IPv6 address pool, obfuscation parameters)
- AWG client management directly from the **Inbounds** page — just like VLESS or Trojan
- Per-client: automatic key generation (private, public, preshared), IP allocation from pool, QR code, `.conf` file download
- Traffic statistics collected every 10 seconds (upload/download per client)
- Traffic limits, expiry dates, auto-renew, IP limit — same as all other protocols

**AmneziaWG 2.0.** The panel supports the new 2.0-generation obfuscation set on top of classic 1.x:
- extra parameters **S3 / S4** (padding for cookie/transport packets) and **I1** (a CPS signature packet before the handshake);
- **H1–H4** now accept not just a single value but a range (`100000-800000`);
- a **Generate** button fills the form with a random valid 2.0 set (*default* and *mobile* presets);
- a **push configs** button sends every Telegram-linked client its config via the bot — so after switching to 2.0 they can re-import their profile in one tap;
- DNS is pushed to clients split by family (IPv4 / IPv6).

A fresh install configures the server in 2.0 mode right away; empty S3/S4/I1 keep classic 1.x output — backward compatibility is preserved.

**AmneziaWG 3.0 / 3.1.** The 3.0 generation answers the blocking wave of mid-2026, where masking individual packet traits stopped being enough: it encrypts the packet header and randomises the protocol timers, so a session keeps no stable profile to recognise. The panel supports the complete set:

- **HeaderProtectionKey** — ChaCha20 encryption of the packet header. The one 3.0 parameter that must match on both ends, generated together with the rest;
- **ContentPaddingAddition** — extra padding inside the encrypted part;
- **RekeyAfterTime**, **RekeyTimeout**, **RejectAfterTime**, **KeepaliveTimeout**, **MaxHandshakeAttempts** — protocol timers, each taking a range (`100-130`) from which the kernel draws a fresh value per session;
- **RandomTrailers** and **DisableCookies** — the two parameters 3.1 added;
- **I2–I5** — the remaining 2.0 signature packets, alongside I1.

The generation is picked from a dropdown next to the **Generate** button: 2.0 or 3.x. Choosing 3.x also produces the 2.0 set, because header protection needs S1–S4 wide enough to carry its nonce and the panel keeps the two halves consistent. The 3.x option is disabled, with the installed version shown next to it, when the server's `amneziawg-tools` or kernel module predate 3.0 — writing those keys there would produce a config that refuses to load. Every field is optional and an unset one is not written at all, so a 1.x or 2.0 server keeps producing exactly the config it produced before.

> Clients must support 3.0 too: after switching, everyone re-imports their config, and an older AmneziaVPN app will not read it.

### 2. MTProto — Telegram proxy (FakeTLS)

The new **MTProto** protocol is a FakeTLS proxy for Telegram, run as a standalone **mtg / mtg-multi** process (not Xray) and managed from the **Inbounds** page like any other protocol.

**How it works:**
- Pick a public port and a **FakeTLS fronting domain** — the connection is disguised as TLS 1.3 to that domain (e.g. `www.cloudflare.com`); a **↻** button next to the field fills in a random domain from a curated list.
- The FakeTLS secret is **generated automatically** and shared as a **`tg://proxy` deep link** + QR — clicking it opens the Telegram app and prompts "Enable proxy?" directly.
- The details window shows the protocol's real security: **FakeTLS**, **MTProto 2.0 (AES-256-IGE)** encryption, the cover domain, and the **mtg/mtg-multi sidecar version**.

**Many users per port (multi-user).** On `amd64` / `arm64` the panel uses the **[mtg-multi](https://github.com/dolonet/mtg-multi)** fork, which serves many clients on a single port — handy behind NAT where you don't want one port-forward per user:
- each client has a **unique UID** and a **free-form name** (several clients may share a name — like AmneziaWG);
- its own FakeTLS secret, link/QR, traffic, quota, expiry, and online status;
- on other architectures the panel transparently falls back to single-secret **mtg** (one client per port). The right binary is fetched by `install.sh` / `update.sh` automatically.

**Anti-block egress.** An optional **Route through Xray** toggle: instead of dialing Telegram directly, mtg goes through a loopback SOCKS bridge that the panel injects into the running Xray config, routed to an **outbound of your choice** (e.g. a chain to a server where Telegram is reachable). Useful when Telegram is blocked on the panel host itself.

Existing MTProto inbounds are **migrated automatically** on the first start after an update — secrets, settings, and recorded traffic are preserved, and old links keep working.

### 3. Native WireGuard with native IPv6

A separate **native WireGuard** protocol (no obfuscation) for when you want clean, maximum-speed WireGuard rather than AmneziaWG. Managed the same way from the **Inbounds** page: multi-client, automatic key generation, QR, `.conf`, statistics, limits, and expiry per peer.

Clients can likewise be given a **native public IPv6** address from the server without NAT66 (via NDP proxy) and have ports forwarded (see sections 5 and 6). Compatible with standard WireGuard clients.

### 4. AmneziaWG obfuscation parameters

The AWG settings page lets you configure packet obfuscation parameters:

| Parameter | Description |
|-----------|-------------|
| `Jc` | Number of junk packets before handshake |
| `Jmin` / `Jmax` | Minimum and maximum size of junk packets |
| `S1` / `S2` | Size of init/response headers |
| `S3` / `S4` | (2.0) padding for cookie and transport packets |
| `H1` – `H4` | Magic headers; in 2.0 they accept a range of values |
| `I1` – `I5` | (2.0) signature packets sent before the handshake |
| `HeaderProtectionKey` | (3.0) ChaCha20 encryption of the packet header — must match on both ends |
| `ContentPaddingAddition` | (3.0) extra padding inside the encrypted part |
| `RekeyAfterTime` / `RekeyTimeout` | (3.0) when a rekey starts, and the pause between handshake attempts |
| `RejectAfterTime` / `KeepaliveTimeout` | (3.0) when a session expires, and the pause before a keepalive |
| `MaxHandshakeAttempts` | (3.0) handshake attempts before giving up |
| `RandomTrailers` | (3.1) random tail appended to packets |
| `DisableCookies` | (3.1) turn off cookie replies |

These parameters are automatically written into each client's config — no manual configuration needed.

### 5. Native IPv6 support without NAT

AWG / native WireGuard clients can be assigned a **native public IPv6 address** from the server — without NAT66. This works via NDP proxy (ndppd or a built-in fallback using `ip -6 neigh add proxy`). Clients receive a real IPv6 address, which matters for services that require it.

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

### 6. Per-client port forwarding for AmneziaWG / native WireGuard

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

### 7. SOCKS5 and HTTP proxies with full per-user infrastructure

xray-core's `mixed` (SOCKS5) and `http` inbounds now share the **same VLESS-style stack** as VLESS / VMess / Trojan / Shadowsocks:
- expandable peer table with per-client traffic, expiry, quota, IP limit, enable toggle;
- standard rich client edit modal (auto-generated 6-character username + 16-character password, regenerable);
- per-user traffic stats flow through xray's standard `user>>>EMAIL>>>traffic>>>...` keys, so the existing traffic and disable-on-quota / disable-on-expiry jobs handle MIXED/HTTP automatically;
- "Add Client" entry in the inbound action menu, just like VLESS.

The username remains editable after creation — renaming a client doesn't reset its traffic counters because the backend renames the underlying `client_traffic` row in place.

### 8. Automatic protocol installation

The install script (`install.sh`) automatically:
- Installs the AmneziaWG kernel module via PPA `ppa:amnezia/ppa`, plus `awg-tools` and `ndppd`
- Detects the server's external interface and configures PostUp/PostDown rules
- Fetches the MTProto sidecar binary (mtg-multi on amd64/arm64, otherwise mtg) from the official releases
- Sets up AWG autostart after server reboot
- Detects Secure Boot and warns about potential DKMS module issues

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

Protocol stacks (AmneziaWG, native WireGuard, MTProto, xray) install normally in debug mode — only the panel's web access is restricted to the loopback.

### 11. Extras

- **Telegram bot:** sends connection links and QR codes straight to the client's chat on creation; pushes AWG configs to linked clients (for the 2.0 migration).
- **Configurable QR code size:** 300 / 450 (default) / 600 px.
- **Secure subscription URL by default:** on install the subscription path is generated with a random 12-character suffix (e.g. `/sub-Xk92mPqLvzRt/`) instead of `/sub/`.

---

## Server requirements

- **OS:** Ubuntu 22.04+ / Debian 11+
- **Linux kernel:** 5.6+ (for built-in WireGuard), or an installed AmneziaWG DKMS module
- **RAM:** 1024 MB or more
- **Architecture:** amd64 / arm64 (multi-user MTProto is available only on these; on other arches MTProto runs in single-secret mode)

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
2. Configure network parameters and obfuscation settings (or click **Generate** for a 2.0 set)
3. Go to **Inbounds** → **Add Inbound**
4. Select the **amneziawg** protocol, enter a client email, and click **Create**
5. In the client table, click the QR code icon and scan it in the AmneziaVPN app

## MTProto (Telegram) quick start

1. **Inbounds** → **Add Inbound** → **MTProto (Telegram)** protocol
2. Set the port and a fronting domain (or click **↻** for a random one), name the first client → **Create**
3. Open the client's QR / link in the table — clicking the `tg://` link opens Telegram and offers to enable the proxy
4. To add more users on the same port, use the add-client button in the inbound's row (on amd64/arm64)

---

## Compatible clients

| Protocol | Client | Platforms |
|----------|--------|-----------|
| AmneziaWG | AmneziaVPN — [amnezia.org](https://amnezia.org) | Android, iOS, Windows, macOS, Linux |
| native WireGuard | Official WireGuard | All platforms |
| MTProto | Telegram (built-in proxy support) | All platforms |

> Standard WireGuard clients are **not compatible** with AmneziaWG — they do not support obfuscation parameters.

---

## Based on

3AX-UI is based on **[3x-ui](https://github.com/MHSanaei/3x-ui)** by [MHSanaei](https://github.com/MHSanaei). All original features (VLESS, VMess, Trojan, Shadowsocks, WireGuard, Xray, subscriptions, Telegram bot, etc.) are fully preserved.

The MTProto proxy runs on the **[mtg](https://github.com/9seconds/mtg)** sidecar (single-secret) and its **[mtg-multi](https://github.com/dolonet/mtg-multi)** fork (multi-user).

## Acknowledgements

- [MHSanaei](https://github.com/MHSanaei/) — author of the original 3x-ui
- [alireza0](https://github.com/alireza0/) — author of the original x-ui
- [9seconds/mtg](https://github.com/9seconds/mtg) and [dolonet/mtg-multi](https://github.com/dolonet/mtg-multi) — MTProto sidecars
- [Iran v2ray rules](https://github.com/chocolate4u/Iran-v2ray-rules) (GPL-3.0)
- [Russia v2ray rules](https://github.com/runetfreedom/russia-v2ray-rules-dat) (GPL-3.0)

---

## License

This project is distributed under the same license as the original 3x-ui — [GNU GPL v3](LICENSE).

// One model for both tunnel flavours the panel manages. AmneziaWG and native
// WireGuard describe the same server; AmneziaWG adds obfuscation and the two
// use different defaults so they can run side by side on one host.
const TUNNEL_KIND_DEFAULTS = {
    awg: {
        interfaceName: 'awg0',
        listenPort: 51820,
        ipv4Address: '10.66.66.1/24',
        ipv4Pool: '10.66.66.0/24',
        xrayInboundTag: 'awg-tproxy-in',
        xrayTproxyPort: 12345,
        obfuscation: true,
    },
    wg: {
        interfaceName: 'wg0',
        listenPort: 51821,
        ipv4Address: '10.77.77.1/24',
        ipv4Pool: '10.77.77.0/24',
        xrayInboundTag: 'wg-tproxy-in',
        xrayTproxyPort: 12346,
        obfuscation: false,
    },
};

class TunnelServer {
    constructor(kind, data = {}) {
        const d = TUNNEL_KIND_DEFAULTS[kind] || TUNNEL_KIND_DEFAULTS.awg;
        this.kind = data.kind || kind;
        this.id = data.id || 0;
        this.enable = data.enable || false;
        this.interfaceName = data.interfaceName || d.interfaceName;
        this.listenPort = data.listenPort || d.listenPort;
        this.mtu = data.mtu || 1420;
        this.privateKey = data.privateKey || '';
        this.publicKey = data.publicKey || '';
        this.ipv4Address = data.ipv4Address || d.ipv4Address;
        this.ipv4Pool = data.ipv4Pool || d.ipv4Pool;
        this.ipv6Enabled = data.ipv6Enabled || false;
        this.ipv6Address = data.ipv6Address || '';
        this.ipv6Pool = data.ipv6Pool || '';
        this.ipv6Gateway = data.ipv6Gateway || '';

        if (d.obfuscation) {
            this.jc = data.jc !== undefined ? data.jc : 4;
            this.jmin = data.jmin !== undefined ? data.jmin : 50;
            this.jmax = data.jmax !== undefined ? data.jmax : 1000;
            this.s1 = data.s1 !== undefined ? data.s1 : 0;
            this.s2 = data.s2 !== undefined ? data.s2 : 0;
            this.s3 = data.s3 !== undefined ? data.s3 : 0;
            this.s4 = data.s4 !== undefined ? data.s4 : 0;
            // H1-H4 are strings: a single value ("1") for 1.x or a "low-high" range for 2.0.
            this.h1 = data.h1 !== undefined ? String(data.h1) : '1';
            this.h2 = data.h2 !== undefined ? String(data.h2) : '2';
            this.h3 = data.h3 !== undefined ? String(data.h3) : '3';
            this.h4 = data.h4 !== undefined ? String(data.h4) : '4';
            this.i1 = data.i1 !== undefined ? data.i1 : '';
            this.i2 = data.i2 !== undefined ? data.i2 : '';
            this.i3 = data.i3 !== undefined ? data.i3 : '';
            this.i4 = data.i4 !== undefined ? data.i4 : '';
            this.i5 = data.i5 !== undefined ? data.i5 : '';
            // AmneziaWG 3.0. Empty means "not written to the config", so the
            // kernel keeps its default and a 2.0 server stays exactly as it was.
            this.headerProtectionKey = data.headerProtectionKey || '';
            this.contentPaddingAddition = data.contentPaddingAddition || '';
            this.rekeyAfterTime = data.rekeyAfterTime || '';
            this.rekeyTimeout = data.rekeyTimeout || '';
            this.rejectAfterTime = data.rejectAfterTime || '';
            this.keepaliveTimeout = data.keepaliveTimeout || '';
            this.maxHandshakeAttempts = data.maxHandshakeAttempts || '';
            this.randomTrailers = data.randomTrailers || false;
            this.disableCookies = data.disableCookies || false;
        }

        this.dnsIpv4 = data.dnsIpv4 || '1.1.1.1';
        this.dnsIpv6 = data.dnsIpv6 || '2606:4700:4700::1111';
        this.externalInterface = data.externalInterface || '';
        this.ipv6ExternalInterface = data.ipv6ExternalInterface || undefined;
        this.postUp = data.postUp || '';
        this.postDown = data.postDown || '';
        this.endpoint = data.endpoint || '';
        this.trafficReset = data.trafficReset || 'never';
        this.routeViaXray = data.routeViaXray || false;
        this.xrayInboundTag = data.xrayInboundTag || d.xrayInboundTag;
        this.xrayTproxyPort = data.xrayTproxyPort || d.xrayTproxyPort;
    }
}

// Clients are identical for both flavours.
class TunnelClient {
    constructor(data = {}) {
        this.id = data.id || 0;
        this.serverId = data.serverId || 0;
        this.name = data.name || '';
        this.email = data.email || '';
        this.enable = data.enable !== undefined ? data.enable : true;
        this.comment = data.comment || '';
        this.privateKey = data.privateKey || '';
        this.publicKey = data.publicKey || '';
        this.presharedKey = data.presharedKey || '';
        this.ipv4Address = data.ipv4Address || '';
        this.ipv6Address = data.ipv6Address || '';
        this.allowedIPs = data.allowedIPs || '';
        this.clientAllowedIPs = data.clientAllowedIPs || '0.0.0.0/0, ::/0';
        this.forwardedPorts = data.forwardedPorts || '';
        this.persistentKeepalive = data.persistentKeepalive !== undefined ? data.persistentKeepalive : 25;
        this.upload = data.upload || 0;
        this.download = data.download || 0;
        this.totalGB = data.totalGB || 0;
        this.allTime = data.allTime || 0;
        this.expiryTime = data.expiryTime || 0;
        this.reset = data.reset || 0;
        this.limitIp = data.limitIp || 0;
        this.tgId = data.tgId || 0;
        this.lastOnline = data.lastOnline || 0;
        this.lastIp = data.lastIp || '';
        this.createdAt = data.createdAt || 0;
        this.updatedAt = data.updatedAt || 0;
    }
}

// Flavour-named wrappers so existing call sites keep reading naturally.
class AwgServer extends TunnelServer {
    constructor(data = {}) { super('awg', data); }
}

class WgServer extends TunnelServer {
    constructor(data = {}) { super('wg', data); }
}

class AwgClient extends TunnelClient {}
class WgClient extends TunnelClient {}

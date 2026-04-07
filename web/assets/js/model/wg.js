class WgServer {
    constructor(data = {}) {
        this.id = data.id || 0;
        this.enable = data.enable || false;
        this.interfaceName = data.interfaceName || 'wg0';
        this.listenPort = data.listenPort || 51821;
        this.mtu = data.mtu || 1420;
        this.privateKey = data.privateKey || '';
        this.publicKey = data.publicKey || '';
        this.ipv4Address = data.ipv4Address || '10.77.77.1/24';
        this.ipv4Pool = data.ipv4Pool || '10.77.77.0/24';
        this.ipv6Enabled = data.ipv6Enabled || false;
        this.ipv6Address = data.ipv6Address || '';
        this.ipv6Pool = data.ipv6Pool || '';
        this.ipv6Gateway = data.ipv6Gateway || '';
        this.dns = data.dns || '1.1.1.1,2606:4700:4700::1111';
        this.externalInterface = data.externalInterface || '';
        this.ipv6ExternalInterface = data.ipv6ExternalInterface || undefined;
        this.postUp = data.postUp || '';
        this.postDown = data.postDown || '';
        this.endpoint = data.endpoint || '';
        this.trafficReset = data.trafficReset || 'never';
    }
}

class WgClient {
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

class AwgServer {
    constructor(data = {}) {
        this.id = data.id || 0;
        this.enable = data.enable || false;
        this.interfaceName = data.interfaceName || 'awg0';
        this.listenPort = data.listenPort || 51820;
        this.mtu = data.mtu || 1420;
        this.privateKey = data.privateKey || '';
        this.publicKey = data.publicKey || '';
        this.ipv4Address = data.ipv4Address || '10.66.66.1/24';
        this.ipv4Pool = data.ipv4Pool || '10.66.66.0/24';
        this.ipv6Enabled = data.ipv6Enabled || false;
        this.ipv6Address = data.ipv6Address || '';
        this.ipv6Pool = data.ipv6Pool || '';
        this.ipv6Gateway = data.ipv6Gateway || '';
        this.jc = data.jc !== undefined ? data.jc : 4;
        this.jmin = data.jmin !== undefined ? data.jmin : 50;
        this.jmax = data.jmax !== undefined ? data.jmax : 1000;
        this.s1 = data.s1 !== undefined ? data.s1 : 0;
        this.s2 = data.s2 !== undefined ? data.s2 : 0;
        this.h1 = data.h1 !== undefined ? data.h1 : 1;
        this.h2 = data.h2 !== undefined ? data.h2 : 2;
        this.h3 = data.h3 !== undefined ? data.h3 : 3;
        this.h4 = data.h4 !== undefined ? data.h4 : 4;
        this.dns = data.dns || '1.1.1.1,2606:4700:4700::1111';
        this.externalInterface = data.externalInterface || '';
        this.ipv6ExternalInterface = data.ipv6ExternalInterface || undefined;
        this.postUp = data.postUp || '';
        this.postDown = data.postDown || '';
        this.endpoint = data.endpoint || '';
        this.trafficReset = data.trafficReset || 'never';
    }
}

class AwgClient {
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

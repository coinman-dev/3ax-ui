import { describe, expect, it } from 'vitest';

import { genInboundLinks } from '@/lib/xray/inbound-link';
import { InboundSchema } from '@/schemas/api/inbound';

// 3AX-UI: mixed (SOCKS5) and HTTP inbounds carry panel clients; each gets a
// socks:// or http:// link whose userinfo is base64("email:password").
function proxyInbound(protocol: 'mixed' | 'http') {
  return InboundSchema.parse({
    id: 71,
    remark: 'proxy',
    port: 1080,
    protocol,
    settings: {
      ...(protocol === 'mixed' ? { auth: 'password', udp: true } : {}),
      accounts: [],
      clients: [
        { email: 'alice', password: 'alicepass', enable: true },
        { email: 'bob', password: 'bobpass', enable: true },
      ],
    },
  });
}

describe('mixed/http client links', () => {
  it.each([
    ['mixed', 'socks'],
    ['http', 'http'],
  ] as const)('%s emits one %s:// link per client', (protocol, scheme) => {
    const out = genInboundLinks({
      inbound: proxyInbound(protocol),
      remark: 'proxy',
      fallbackHostname: 'proxy.example.test',
    });
    const links = out.split('\r\n').filter(Boolean);
    expect(links).toHaveLength(2);
    expect(links[0]).toBe(`${scheme}://${btoa('alice:alicepass')}@proxy.example.test:1080#proxy`);
    expect(links[1]).toBe(`${scheme}://${btoa('bob:bobpass')}@proxy.example.test:1080#proxy`);
  });
});

import { describe, expect, it } from 'vitest';

import { inboundFromDb } from '@/lib/xray/inbound-from-db';
import { genInboundLinks } from '@/lib/xray/inbound-link';

// 3AX-UI: nginx moves an inbound to a loopback port; the panel's links and QR
// codes must still carry the public port clients dial.
describe('links of an inbound behind nginx', () => {
  const relocated = {
    port: 18443,
    publicPort: 443,
    listen: '127.0.0.1',
    protocol: 'vless',
    remark: 'behind443',
    settings: JSON.stringify({
      clients: [{ id: '11111111-2222-4333-8444-555555555555', email: 'a@test', flow: '' }],
      decryption: 'none',
    }),
    streamSettings: JSON.stringify({ network: 'tcp', security: 'none' }),
    sniffing: '',
  };

  it('advertise the public port, not the loopback one', () => {
    const links = genInboundLinks({
      inbound: inboundFromDb(relocated),
      fallbackHostname: 'panel.example.test',
    });
    expect(links).toContain('@panel.example.test:443?');
    expect(links).not.toContain(':18443');
  });

  it('keep the own port when nothing is in front', () => {
    const links = genInboundLinks({
      inbound: inboundFromDb({ ...relocated, publicPort: 0 }),
      fallbackHostname: 'panel.example.test',
    });
    expect(links).toContain('@panel.example.test:18443?');
  });
});

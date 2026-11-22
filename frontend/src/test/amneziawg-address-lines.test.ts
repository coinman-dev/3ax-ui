import { describe, expect, it } from 'vitest';

import { genAmneziaWGConfig } from '@/lib/xray/inbound-link';
import { buildAmneziaWGClientConfig } from '@/pages/clients/amneziawgConfig';
import type { AmneziawgInboundSettings } from '@/schemas/protocols/inbound/amneziawg';
import type { ClientRecord, InboundOption } from '@/hooks/useClients';

// The AmneziaVPN importer keeps only the last Address line, and its Linux/macOS daemon parses
// that as one IPv4 subnet: a comma list or a trailing IPv6 line connects with no traffic.
const WANT = ['Address = 2001:db8::2/128', 'Address = 10.66.66.2/32'];

function addressLines(conf: string): string[] {
  return conf.split('\n').filter((line) => line.startsWith('Address'));
}

const server = {
  publicKey: 'serverPubKey==',
  primaryDns: '',
  secondaryDns: '',
  mtu: 1420,
  jc: 4,
  jmin: 40,
  jmax: 100,
  s1: 30,
  s2: 90,
  s3: 0,
  s4: 0,
  h1: '',
  h2: '',
  h3: '',
  h4: '',
};

describe('AmneziaWG .conf puts IPv4 on the last Address line', () => {
  it.each([
    ['IPv4 stored first', ['10.66.66.2/32', '2001:db8::2/128']],
    ['IPv6 stored first', ['2001:db8::2/128', '10.66.66.2/32']],
  ])('share-link emitter, %s', (_name, allowedIPs) => {
    const settings = {
      server,
      clients: [{ email: 'peer-1', privateKey: 'clientPrivKey==', allowedIPs }],
    } as unknown as AmneziawgInboundSettings;
    const conf = genAmneziaWGConfig({
      settings,
      address: 'awg.example.test',
      port: 51820,
      remark: 'awg-peer-1',
      peerIndex: 0,
    });
    expect(addressLines(conf)).toEqual(WANT);
  });

  it('clients-page emitter, from the stored comma list', () => {
    const client = {
      email: 'peer-1',
      privateKey: 'clientPrivKey==',
      allowedIPs: '10.66.66.2/32,2001:db8::2/128',
    } as unknown as ClientRecord;
    const inbound = {
      id: 1,
      tag: 'awg-1',
      remark: 'awg',
      port: 51820,
      protocol: 'amneziawg',
      awgServer: server,
    } as unknown as InboundOption;
    const conf = buildAmneziaWGClientConfig(client, inbound, 'awg.example.test');
    expect(addressLines(conf)).toEqual(WANT);
  });
});

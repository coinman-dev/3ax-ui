import { describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';

import { AxAwgIpv6Clients } from '@/pages/inbounds/list/AxAwgIpv6Clients';
import { renderWithProviders } from './test-utils';

// The AmneziaVPN desktop app drops tunnel IPv6, so an IPv6 AmneziaWG inbound names the clients
// that keep it; on an inbound without IPv6 the hint would only mislead.
describe('AmneziaWG IPv6 client hint', () => {
  it('links the IPv6-capable clients in a new window when the inbound serves IPv6', () => {
    renderWithProviders(
      <AxAwgIpv6Clients settings={JSON.stringify({ server: { ipv6Enabled: true } })} />,
    );
    const links = screen.getAllByRole('link');
    expect(
      links.map((a) => [a.textContent, a.getAttribute('href'), a.getAttribute('target')]),
    ).toEqual([
      ['Win', 'https://github.com/amnezia-vpn/amneziawg-windows-client', '_blank'],
      ['Linux', 'https://github.com/coinman-dev/awg-linux-client', '_blank'],
    ]);
  });

  it('renders nothing for an inbound without IPv6', () => {
    const { container } = renderWithProviders(
      <AxAwgIpv6Clients settings={{ server: { ipv6Enabled: false } }} />,
    );
    expect(container.textContent).toBe('');
  });
});

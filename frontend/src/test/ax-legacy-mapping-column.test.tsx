import { expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';

import AxInboundClientsTable from '@/pages/inbounds/legacy/AxInboundClientsTable';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

const clients = [
  {
    email: 'lenya-pc',
    enable: true,
    inboundIds: [3],
    totalGB: 0,
    expiryTime: 0,
    forwardedPorts: '37015',
    traffic: { up: 0, down: 0 },
  },
  {
    email: 'modulator-pc',
    enable: true,
    inboundIds: [3],
    totalGB: 0,
    expiryTime: 0,
    traffic: { up: 0, down: 0 },
  },
];
vi.mock('@/hooks/useClients', () => ({
  useClients: () => ({
    clients,
    filtered: clients.length,
    fetched: true,
    transitioning: false,
    setQuery: () => {},
    pageSize: 50,
    settingsReady: true,
    expireDiff: 0,
    trafficDiff: 0,
    hydrate: vi.fn(),
    setEnable: vi.fn(),
    resetTraffic: vi.fn(),
    remove: vi.fn(),
    detach: vi.fn(),
    applyClientStatsEvent: () => {},
  }),
}));
vi.mock('@/hooks/useWebSocket', () => ({ useWebSocket: () => {} }));

function renderTable(showForwardedPorts: boolean) {
  vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', { datepicker: 'gregorian' }));
  renderWithProviders(
    <AxInboundClientsTable
      inboundId={3}
      showForwardedPorts={showForwardedPorts}
      onlineEmails={[]}
      isMobile={false}
      onOpen={() => {}}
    />,
  );
}

// 3AX-UI: 1.x showed an AmneziaWG client's forwarded ports in a Mapping column between
// the client and its traffic; 2.0 had them only inside the client editor.
it('shows forwarded ports in a Mapping column between Client and Traffic', async () => {
  renderTable(true);
  const lenya = (await screen.findByText('lenya-pc')).closest('tr') as HTMLElement;
  const headers = screen.getAllByRole('columnheader').map((h) => h.textContent);
  const client = headers.indexOf('Client');
  expect(headers.slice(client, client + 3)).toEqual(['Client', 'Mapping', 'Traffic']);
  expect(within(lenya).getByText('37015')).toBeTruthy();
  const modulator = screen.getByText('modulator-pc').closest('tr') as HTMLElement;
  expect(within(modulator).getByText('—')).toBeTruthy();
});

it('has no Mapping column on inbounds without port forwarding', async () => {
  renderTable(false);
  await screen.findByText('lenya-pc');
  expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).not.toContain('Mapping');
  expect(screen.queryByText('37015')).toBeNull();
});

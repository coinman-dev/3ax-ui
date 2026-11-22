import { expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';

import AxInboundClientsTable from '@/pages/inbounds/legacy/AxInboundClientsTable';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

const { detach } = vi.hoisted(() => ({ detach: vi.fn() }));
const clients = [
  {
    email: 'shared@x',
    enable: true,
    inboundIds: [3, 7],
    totalGB: 0,
    expiryTime: 0,
    traffic: { up: 0, down: 0 },
  },
  {
    email: 'only-here@x',
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
    detach,
    applyClientStatsEvent: () => {},
  }),
}));
vi.mock('@/hooks/useWebSocket', () => ({ useWebSocket: () => {} }));

// 3AX-UI: as in 1.x, a client can leave one inbound and keep the others; a client on a
// single inbound has no such button, since Delete already does exactly that.
it('removes a shared client from this inbound only', async () => {
  detach.mockResolvedValue(new Msg(true, '', null));
  vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', { datepicker: 'gregorian' }));
  renderWithProviders(
    <AxInboundClientsTable
      inboundId={3}
      showForwardedPorts={false}
      onlineEmails={[]}
      isMobile={false}
      onOpen={() => {}}
    />,
  );
  const sharedRow = (await screen.findByText('shared@x')).closest('tr') as HTMLElement;
  const onlyRow = screen.getByText('only-here@x').closest('tr') as HTMLElement;
  expect(within(onlyRow).queryByRole('button', { name: /remove from this inbound/i })).toBeNull();
  fireEvent.click(within(sharedRow).getByRole('button', { name: /remove from this inbound/i }));
  const confirm = await screen.findByRole('dialog');
  fireEvent.click(within(confirm).getByRole('button', { name: /remove/i }));
  await waitFor(() => expect(detach).toHaveBeenCalledWith('shared@x', [3]));
});

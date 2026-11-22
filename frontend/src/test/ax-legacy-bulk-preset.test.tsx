import { expect, it, vi } from 'vitest';
import { fireEvent, waitFor } from '@testing-library/react';

import ClientBulkAddModal from '@/pages/clients/ClientBulkAddModal';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

const { bulkCreate } = vi.hoisted(() => ({ bulkCreate: vi.fn() }));
vi.mock('@/hooks/useClients', () => ({ useClients: () => ({ bulkCreate }) }));

it('creates bulk clients on the inbound the 1.x inbounds page opened the modal for', async () => {
  bulkCreate.mockResolvedValue(new Msg(true, '', { created: 1, skipped: [] }));
  vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', { datepicker: 'gregorian' }));
  renderWithProviders(
    <ClientBulkAddModal
      open
      inbounds={[
        { id: 1, protocol: 'vless', tag: 'first' },
        { id: 2, protocol: 'vless', tag: 'second' },
      ]}
      presetInboundIds={[2]}
      onOpenChange={() => {}}
    />,
  );
  const button = document.querySelector('.ant-modal-footer .ant-btn-primary');
  if (!button) throw new Error('Create button missing');
  fireEvent.click(button);
  await waitFor(() => expect(bulkCreate).toHaveBeenCalled());
  const payloads = bulkCreate.mock.calls[0][0] as { inboundIds: number[] }[];
  expect(payloads.map((p) => p.inboundIds)).toEqual([[2]]);
});

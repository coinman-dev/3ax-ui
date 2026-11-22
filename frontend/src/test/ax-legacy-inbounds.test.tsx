import { readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import ClientFormModal from '@/pages/clients/ClientFormModal';
import type { InboundOption } from '@/hooks/useClients';
import { PANEL_ROUTES } from '@/routes/panelRoutes';
import { AX_INBOUNDS_AND_CLIENTS_TITLE_KEY, axNavRoutes, axTitleKey } from '@/routes/axLegacyNav';
import { axClientBucket, axNestedQuery } from '@/pages/inbounds/legacy/axInboundClients';
import { renderWithProviders } from './test-utils';

const enUS = JSON.parse(
  readFileSync(
    join(resolve(process.cwd(), '..'), 'internal', 'web', 'translation', 'en-US.json'),
    'utf8',
  ),
) as { menu: Record<string, string> };

describe('3AX-UI legacy inbounds navigation', () => {
  it('leaves the upstream menu untouched while the setting is off', () => {
    expect(axNavRoutes(PANEL_ROUTES, false)).toBe(PANEL_ROUTES);
  });

  it('folds Clients into a renamed Inbounds entry while the setting is on', () => {
    const routes = axNavRoutes(PANEL_ROUTES, true);
    expect(routes.map((r) => r.path)).toEqual(
      PANEL_ROUTES.map((r) => r.path).filter((p) => p !== '/clients'),
    );
    expect(routes.find((r) => r.path === '/inbounds')?.titleKey).toBe(
      AX_INBOUNDS_AND_CLIENTS_TITLE_KEY,
    );
    expect(enUS.menu.inboundsAndClients).toBe('Inbounds and clients');
  });

  it('titles the pages after the entry the menu shows', () => {
    expect(axTitleKey('/inbounds', true)).toBe(AX_INBOUNDS_AND_CLIENTS_TITLE_KEY);
    expect(axTitleKey('/inbounds', false)).toBe('menu.inbounds');
    expect(axTitleKey('/clients', true)).toBe('menu.clients');
  });
});

describe('3AX-UI nested clients table', () => {
  const now = 1_800_000_000_000;
  const day = 86_400_000;
  const gb = 1024 ** 3;

  it.each([
    ['expired beats disabled', { enable: false, expiryTime: now - 1 }, 'depleted'],
    ['quota used up', { enable: true, totalGB: gb, traffic: { up: gb, down: 0 } }, 'depleted'],
    ['disabled', { enable: false, expiryTime: now + 30 * day }, 'deactive'],
    ['expires inside expireDiff', { enable: true, expiryTime: now + day }, 'expiring'],
    [
      'quota left under trafficDiff',
      { enable: true, totalGB: 10 * gb, traffic: { up: 9.5 * gb, down: 0 } },
      'expiring',
    ],
    ['healthy', { enable: true, expiryTime: now + 30 * day, totalGB: 10 * gb }, 'active'],
  ] as const)('classifies a client: %s', (_name, row, bucket) => {
    expect(axClientBucket(row, now, 3 * day, gb)).toBe(bucket);
  });

  it('asks for one inbound and stays within the server page cap', () => {
    expect(axNestedQuery(7, 2, 25)).toEqual({ page: 2, pageSize: 25, inbound: '7' });
    expect(axNestedQuery(7, 1, 0).pageSize).toBe(200);
    expect(axNestedQuery(7, 1, 500).pageSize).toBe(200);
  });
});

describe('ClientFormModal opened from an inbound row', () => {
  it('creates the client on the inbound it was opened for', async () => {
    const save = vi.fn().mockResolvedValue({ success: true });
    const inbound = { id: 4, port: 443, protocol: 'vless', tag: 'in-443', enable: true };
    renderWithProviders(
      <ClientFormModal
        open
        mode="add"
        client={null}
        inbounds={[inbound as unknown as InboundOption]}
        attachedIds={[4]}
        save={save}
        onOpenChange={() => {}}
      />,
    );
    fireEvent.click(await screen.findByRole('button', { name: /create|add|save/i }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect((save.mock.calls[0][0] as { inboundIds: number[] }).inboundIds).toEqual([4]);
  });
});

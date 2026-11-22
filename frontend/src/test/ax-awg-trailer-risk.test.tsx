import { Form } from 'antd';
import { FormProvider, useForm, useWatch } from 'react-hook-form';
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import { axAwgTrailerLoss } from '@/lib/xray/ax-awg-trailer-risk';
import AmneziawgFields from '@/pages/inbounds/form/protocols/amneziawg';
import { AxAwgTrailerRiskTag } from '@/pages/inbounds/list/AxAwgTrailerRiskTag';
import { HttpUtil, Msg } from '@/utils';
import { makeTestQueryClient, renderWithProviders } from './test-utils';

// The set a 3AX-UI 1.x server carried over: wide H ranges, unequal S1-S4, trailers on.
const migrated = {
  s1: 79,
  s2: 34,
  s3: 51,
  s4: 17,
  h1: '359889474-517213770',
  h2: '737252302-755895719',
  h3: '1295391332-1603192002',
  h4: '2019263529-2089048385',
  randomTrailers: true,
};

// With trailers on, a receiver tries the handshake types first and reads each at its own
// S offset; when that is not S4 it reads random bytes, so a wide H range claims data packets.
describe('axAwgTrailerLoss', () => {
  it('is the share of data packets the H1-H3 ranges claim', () => {
    const p = (lo: number, hi: number) => (hi - lo + 1) / 2 ** 32;
    const want =
      1 -
      (1 - p(359889474, 517213770)) *
        (1 - p(737252302, 755895719)) *
        (1 - p(1295391332, 1603192002));
    expect(axAwgTrailerLoss(migrated)).toBeCloseTo(want, 12);
    expect(axAwgTrailerLoss(migrated)).toBeCloseTo(0.11, 2);
  });

  it('is zero without random trailers', () => {
    expect(axAwgTrailerLoss({ ...migrated, randomTrailers: false })).toBe(0);
  });

  it('skips a handshake type whose S equals S4: it then reads the real H4 type', () => {
    const h1Only = (1517213770 - 1359889474 + 1) / 2 ** 32;
    const equalS2S3 = {
      ...migrated,
      h1: '1359889474-1517213770',
      s2: 17,
      s3: 17,
    };
    expect(axAwgTrailerLoss(equalS2S3)).toBeCloseTo(h1Only, 12);
    expect(axAwgTrailerLoss({ ...migrated, s1: 17, s2: 17, s3: 17 })).toBe(0);
  });

  it('is negligible for single-value headers, what the panel generates', () => {
    const single = { ...migrated, h1: '359889474', h2: '737252302', h3: '1295391332' };
    expect(axAwgTrailerLoss(single)).toBeLessThan(1e-9);
  });
});

interface Server {
  kernelMode: boolean;
  s1: number;
  s2: number;
  s3: number;
  s4: number;
  h1: string;
  h2: string;
  h3: string;
  h4: string;
  randomTrailers: boolean;
}

function HeaderValues() {
  const h = useWatch({
    name: ['settings.server.h1', 'settings.server.h2', 'settings.server.h3', 'settings.server.h4'],
  });
  return <output data-testid="h">{h.join(' ')}</output>;
}

function Harness({ server }: { server: Server }) {
  const form = useForm<{ nodeId: number | null; settings: { server: Server } }>({
    defaultValues: { nodeId: null, settings: { server } },
  });
  return (
    <FormProvider {...form}>
      <Form>
        <AmneziawgFields
          awgPubKey="pub"
          regenInboundAwg={() => {}}
          regenInboundAwgObfuscation={() => {}}
        />
        <HeaderValues />
      </Form>
    </FormProvider>
  );
}

describe('AmneziaWG inbound form: random-trailer packet loss', () => {
  it('warns about the loss and narrows H1-H4 to single values on request', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', { ready: true, blockers: [] }));
    renderWithProviders(<Harness server={{ kernelMode: true, ...migrated }} />, {
      queryClient: makeTestQueryClient(),
    });

    const alert = await screen.findByText(/drops about 11\.0% of large data packets/);
    expect(alert).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Use single-value H1–H4' }));

    await waitFor(() =>
      expect(screen.getByTestId('h').textContent).toBe('359889474 737252302 1295391332 2019263529'),
    );
    expect(screen.queryByText(/of large data packets/)).toBeNull();
  });

  it('stays quiet for single-value headers', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', { ready: true, blockers: [] }));
    renderWithProviders(
      <Harness server={{ kernelMode: true, ...migrated, h1: '5', h2: '6', h3: '7', h4: '8' }} />,
      { queryClient: makeTestQueryClient() },
    );
    await screen.findByText('Kernel mode');
    expect(screen.queryByText(/of large data packets/)).toBeNull();
  });
});

describe('inbound list: random-trailer packet loss tag', () => {
  it('names the loss on an affected AmneziaWG inbound', () => {
    renderWithProviders(<AxAwgTrailerRiskTag settings={JSON.stringify({ server: migrated })} />);
    expect(screen.getByText('~11.0% packet loss')).toBeTruthy();
  });

  it('renders nothing for a safe inbound', () => {
    const { container } = renderWithProviders(
      <AxAwgTrailerRiskTag settings={{ server: { ...migrated, randomTrailers: false } }} />,
    );
    expect(container.textContent).toBe('');
  });
});

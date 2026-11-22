import { screen } from '@testing-library/react';
import { expect, it } from 'vitest';

import { Status } from '@/models/status';
import OverviewActionBar from '@/pages/index/OverviewActionBar';

import { renderWithProviders } from './test-utils';

const noop = () => {};

function renderBar(status: Status) {
  renderWithProviders(
    <OverviewActionBar
      status={status}
      isMobile={false}
      accessLogEnable={false}
      panelVersion="v2.0.0-beta.3"
      latestVersion=""
      updateAvailable={false}
      onStopXray={noop}
      onRestartXray={noop}
      onOpenLogs={noop}
      onOpenXrayLogs={noop}
      onOpenAmneziaWGLogs={noop}
      onOpenConfig={noop}
      onOpenBackup={noop}
      onOpenSystemHistory={noop}
      onOpenXrayMetrics={noop}
      onOpenPanelUpdate={noop}
      onOpenVersionSwitch={noop}
    />,
  );
}

// 3AX-UI: 1.x named the AmneziaWG version next to Xray's; 2.0 showed only Xray and MTProto.
it('names the AmneziaWG engine version when AWG inbounds exist', () => {
  renderBar(new Status({ awg: { configured: 1, running: 1, version: '3.1.20260812' } }));
  expect(screen.getByText('AWG v3.1.20260812')).toBeTruthy();
});

it('shows no AWG tag on a panel without AmneziaWG inbounds', () => {
  renderBar(new Status({ awg: { configured: 0, running: 0, version: '' } }));
  expect(screen.queryByText(/^AWG/)).toBeNull();
});

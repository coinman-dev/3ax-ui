import { useTranslation } from 'react-i18next';
import { Tag, Tooltip } from 'antd';

import { coerceInboundJsonField } from '@/models/dbinbound';
import {
  AX_AWG_TRAILER_LOSS_THRESHOLD,
  type AwgTrailerRiskFields,
  axAwgTrailerLoss,
  formatAwgTrailerLoss,
} from '@/lib/xray/ax-awg-trailer-risk';

// 3AX-UI: a 1.x obfuscation set keeps losing data packets until its H ranges are narrowed.
export function AxAwgTrailerRiskTag({ settings }: { settings: unknown }) {
  const { t } = useTranslation();
  const server = coerceInboundJsonField(
    coerceInboundJsonField(settings).server,
  ) as AwgTrailerRiskFields;
  const loss = axAwgTrailerLoss(server);
  if (loss < AX_AWG_TRAILER_LOSS_THRESHOLD) return null;
  return (
    <Tooltip title={t('pages.xray.amneziawg.axTrailerRiskDesc')}>
      <Tag color="red">
        {t('pages.inbounds.axAwgTrailerRisk', { percent: formatAwgTrailerLoss(loss) })}
      </Tag>
    </Tooltip>
  );
}

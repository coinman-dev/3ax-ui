import { useTranslation } from 'react-i18next';
import { useFormContext, useWatch } from 'react-hook-form';
import { Alert, Button } from 'antd';

import {
  AX_AWG_TRAILER_LOSS_THRESHOLD,
  type AwgTrailerRiskFields,
  axAwgSingleValueHeaders,
  axAwgTrailerLoss,
  formatAwgTrailerLoss,
} from '@/lib/xray/ax-awg-trailer-risk';

const FIELDS = ['s1', 's2', 's3', 's4', 'h1', 'h2', 'h3', 'h4', 'randomTrailers'] as const;

export function AxAwgTrailerRiskAlert() {
  const { t } = useTranslation();
  const { setValue } = useFormContext();
  const values = useWatch({ name: FIELDS.map((f) => `settings.server.${f}`) });
  const server = Object.fromEntries(FIELDS.map((f, i) => [f, values[i]])) as AwgTrailerRiskFields;
  const loss = axAwgTrailerLoss(server);
  if (loss < AX_AWG_TRAILER_LOSS_THRESHOLD) return null;

  const narrow = () => {
    for (const [key, value] of Object.entries(axAwgSingleValueHeaders(server))) {
      setValue(`settings.server.${key}`, value, { shouldDirty: true });
    }
  };
  return (
    <Alert
      type="error"
      showIcon
      style={{ marginBottom: 16 }}
      title={t('pages.xray.amneziawg.axTrailerRiskTitle', { percent: formatAwgTrailerLoss(loss) })}
      description={t('pages.xray.amneziawg.axTrailerRiskDesc')}
      action={
        <Button size="small" onClick={narrow}>
          {t('pages.xray.amneziawg.axTrailerRiskFix')}
        </Button>
      }
    />
  );
}

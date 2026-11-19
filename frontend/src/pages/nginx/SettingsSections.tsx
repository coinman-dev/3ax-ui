import { useTranslation } from 'react-i18next';
import { Alert, Badge, Input, InputNumber, Radio, Switch, Tooltip } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { SettingListItem } from '@/components/ui';
import { NginxSettingsSchema, type NginxMode, type NginxSettings } from '@/schemas/api/nginx';

const MODE_DESC_KEYS: Record<NginxMode, string> = {
  off: 'pages.nginx.modeOffDesc',
  shared: 'pages.nginx.modeSharedDesc',
  only443: 'pages.nginx.modeOnly443Desc',
};

export function ModeSection({ canEnable }: { canEnable: boolean }) {
  const { t } = useTranslation();
  const { control, setValue } = useFormContext<NginxSettings>();
  const mode = useWatch({ control, name: 'mode' });

  // The switches below only exist in some modes; leaving one on would apply it anyway.
  const onModeChange = (next: unknown) => {
    if (next === 'off') setValue('subsBehind443', false);
    if (next !== 'only443') {
      setValue('panelBehind443', false);
      setValue('manageFirewall', false);
    }
  };

  return (
    <>
      <Alert type="info" showIcon title={t('pages.nginx.udpNote')} style={{ marginBottom: 12 }} />
      <SettingListItem
        paddings="small"
        title={t('pages.nginx.mode')}
        description={t(MODE_DESC_KEYS[mode])}
      >
        <FormField name="mode" noStyle onAfterChange={onModeChange}>
          <Radio.Group
            aria-label={t('pages.nginx.mode')}
            optionType="button"
            buttonStyle="solid"
            options={[
              { value: 'off', label: t('pages.nginx.modeOff') },
              {
                value: 'shared',
                disabled: !canEnable,
                label: (
                  <Tooltip title={t('pages.nginx.modeSharedHint')}>
                    {t('pages.nginx.modeShared')}
                  </Tooltip>
                ),
              },
              {
                value: 'only443',
                disabled: !canEnable,
                label: (
                  <Tooltip title={t('pages.nginx.modeOnly443Hint')}>
                    {t('pages.nginx.modeOnly443')}
                  </Tooltip>
                ),
              },
            ]}
          />
        </FormField>
      </SettingListItem>
      {mode !== 'off' && (
        <SettingListItem
          paddings="small"
          title={t('pages.nginx.realityPort')}
          description={t('pages.nginx.realityPortDesc')}
        >
          <FormField
            name="realityPort"
            style={{ marginBottom: 0 }}
            rules={{ validate: rhfZodValidate(NginxSettingsSchema.shape.realityPort) }}
          >
            <InputNumber
              aria-label={t('pages.nginx.realityPort')}
              min={1}
              max={65535}
              precision={0}
            />
          </FormField>
        </SettingListItem>
      )}
    </>
  );
}

export interface CertificateState {
  checking: boolean;
  certOk: boolean;
  certFile: string;
  certExpiry: number;
}

export function DomainSection({ cert }: { cert: CertificateState }) {
  const { t } = useTranslation();
  const { control } = useFormContext<NginxSettings>();
  const domain = useWatch({ control, name: 'domain' });

  let certBadge;
  if (cert.checking) {
    certBadge = <Badge status="processing" text={t('loading')} />;
  } else if (cert.certOk) {
    const label = cert.certExpiry
      ? t('pages.nginx.certExpires', { date: new Date(cert.certExpiry).toISOString().slice(0, 10) })
      : t('pages.nginx.certOk');
    certBadge = <Badge status="success" text={label} />;
  } else {
    certBadge = <Badge status="warning" text={t('pages.nginx.certMissing')} />;
  }

  return (
    <>
      <SettingListItem
        paddings="small"
        title={t('pages.nginx.domain')}
        description={t('pages.nginx.domainDesc')}
      >
        <FormField name="domain" noStyle>
          <Input aria-label={t('pages.nginx.domain')} placeholder="panel.example.com" />
        </FormField>
      </SettingListItem>
      {domain.trim() !== '' && (
        <SettingListItem
          paddings="small"
          title={t('pages.nginx.certificate')}
          description={cert.certFile || '—'}
        >
          {certBadge}
        </SettingListItem>
      )}
    </>
  );
}

export function PublishSection({ certOk }: { certOk: boolean }) {
  const { t } = useTranslation();
  const { control } = useFormContext<NginxSettings>();
  const [mode, domain] = useWatch({ control, name: ['mode', 'domain'] });
  // The site that would carry them is the one the domain's certificate is for.
  const canPublish = mode !== 'off' && domain.trim() !== '' && certOk;
  const canPublishPanel = canPublish && mode === 'only443';

  let subsHint = t('pages.nginx.subsPublishDesc');
  if (mode === 'off') subsHint = t('pages.nginx.subsPublishNeedsMode');
  else if (!canPublish) subsHint = t('pages.nginx.subsPublishNeedsDomain');

  return (
    <>
      <SettingListItem paddings="small" title={t('pages.nginx.subsPublish')} description={subsHint}>
        <FormField name="subsBehind443" valueProp="checked" noStyle>
          <Switch aria-label={t('pages.nginx.subsPublish')} disabled={!canPublish} />
        </FormField>
      </SettingListItem>
      <SettingListItem
        paddings="small"
        title={t('pages.nginx.panelPublish')}
        description={t(
          canPublishPanel ? 'pages.nginx.panelPublishDesc' : 'pages.nginx.panelPublishNeedsOnly443',
        )}
      >
        <FormField name="panelBehind443" valueProp="checked" noStyle>
          <Switch aria-label={t('pages.nginx.panelPublish')} disabled={!canPublishPanel} />
        </FormField>
      </SettingListItem>
    </>
  );
}

export function FirewallSection() {
  const { t } = useTranslation();
  const { control } = useFormContext<NginxSettings>();
  const [mode, manageFirewall] = useWatch({ control, name: ['mode', 'manageFirewall'] });

  return (
    <>
      <SettingListItem
        paddings="small"
        title={t('pages.nginx.firewallSwitch')}
        description={t(
          mode === 'only443' ? 'pages.nginx.firewallDesc' : 'pages.nginx.firewallNeedsOnly443',
        )}
      >
        <FormField name="manageFirewall" valueProp="checked" noStyle>
          <Switch aria-label={t('pages.nginx.firewallSwitch')} disabled={mode !== 'only443'} />
        </FormField>
      </SettingListItem>
      {manageFirewall && (
        <SettingListItem
          paddings="small"
          title={t('pages.nginx.firewallExtra')}
          description={t('pages.nginx.firewallExtraDesc')}
        >
          <FormField name="firewallExtra" noStyle>
            <Input aria-label={t('pages.nginx.firewallExtra')} placeholder="53, 8080, 9000-9100" />
          </FormField>
        </SettingListItem>
      )}
    </>
  );
}

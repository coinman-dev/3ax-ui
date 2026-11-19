import { useTranslation } from 'react-i18next';
import { Button, Input, InputNumber, Select, Space, Switch } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useFormContext, useWatch } from 'react-hook-form';
import type { TextAreaProps } from 'antd/es/input';
import type { SwitchProps } from 'antd';

import { FormField } from '@/components/form/rhf';
import { useOutboundTags } from '@/api/queries/useOutboundTags';

// One URL per line in the form, a string list in the stored settings.
function UrlLines({
  value,
  onChange,
  ...rest
}: Omit<TextAreaProps, 'value' | 'onChange'> & {
  value?: string[];
  onChange?: (v: string[]) => void;
}) {
  return (
    <Input.TextArea
      {...rest}
      autoSize={{ minRows: 2, maxRows: 6 }}
      value={(value ?? []).join('\n')}
      onChange={(e) =>
        onChange?.(
          e.target.value
            .split('\n')
            .map((s) => s.trim())
            .filter(Boolean),
        )
      }
    />
  );
}

// mtg runs anti-replay and the blocklist unless told otherwise, so unset reads as on.
function DefaultOnSwitch({
  value,
  onChange,
  ...rest
}: Omit<SwitchProps, 'checked' | 'onChange' | 'value'> & {
  value?: boolean;
  onChange?: (v: boolean) => void;
}) {
  return <Switch {...rest} checked={value !== false} onChange={(v) => onChange?.(v)} />;
}

export default function MtprotoFields() {
  const { t } = useTranslation();
  const { control, setValue, getValues } = useFormContext();
  const routeThroughXray = useWatch({ control, name: 'settings.routeThroughXray' }) as
    | boolean
    | undefined;
  const { data: outboundTags } = useOutboundTags({ excludeBlackhole: true });
  return (
    <>
      <FormField
        name={['settings', 'fakeTlsDomain']}
        label={t('pages.inbounds.form.fakeTlsDomain')}
        tooltip={t('pages.inbounds.form.mtprotoFakeTlsDomainHint')}
      >
        <Input placeholder="www.cloudflare.com" />
      </FormField>
      <FormField
        name={['settings', 'domainFronting', 'ip']}
        label={t('pages.inbounds.form.mtgDomainFrontingIp')}
        tooltip={t('pages.inbounds.form.mtgDomainFrontingHint')}
      >
        <Input placeholder="127.0.0.1" />
      </FormField>
      <FormField
        name={['settings', 'domainFronting', 'port']}
        label={t('pages.inbounds.form.mtgDomainFrontingPort')}
      >
        <InputNumber min={0} max={65535} placeholder="443" style={{ width: '100%' }} />
      </FormField>
      <FormField
        name={['settings', 'domainFronting', 'proxyProtocol']}
        label={t('pages.inbounds.form.mtgDomainFrontingProxyProtocol')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      <FormField
        name={['settings', 'proxyProtocolListener']}
        label={t('pages.inbounds.form.mtgProxyProtocolListener')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      <FormField name={['settings', 'preferIp']} label={t('pages.inbounds.form.mtgPreferIp')}>
        <Select
          allowClear
          placeholder="prefer-ipv6"
          options={[
            { value: 'prefer-ipv6', label: 'prefer-ipv6' },
            { value: 'prefer-ipv4', label: 'prefer-ipv4' },
            { value: 'only-ipv6', label: 'only-ipv6' },
            { value: 'only-ipv4', label: 'only-ipv4' },
          ]}
        />
      </FormField>
      <FormField
        name={['settings', 'debug']}
        label={t('pages.inbounds.form.mtgDebug')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      <FormField
        name={['settings', 'throttleMaxConnections']}
        label={t('pages.inbounds.form.mtgThrottleMaxConnections')}
        tooltip={t('pages.inbounds.form.mtgThrottleMaxConnectionsHint')}
      >
        <InputNumber min={0} placeholder="0" style={{ width: '100%' }} />
      </FormField>
      <FormField
        name={['settings', 'pendingHandshakesMaxPerIp']}
        label={t('pages.inbounds.form.mtgPendingHandshakesMaxPerIp')}
        tooltip={t('pages.inbounds.form.mtgPendingHandshakesMaxPerIpHint')}
      >
        <InputNumber min={0} placeholder="0" style={{ width: '100%' }} />
      </FormField>
      <FormField
        name={['settings', 'routeThroughXray']}
        label={t('pages.inbounds.form.mtgRouteThroughXray')}
        tooltip={t('pages.inbounds.form.mtgRouteThroughXrayHint')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      {routeThroughXray && (
        <FormField
          name={['settings', 'outboundTag']}
          label={t('pages.inbounds.form.mtgRouteOutbound')}
          tooltip={t('pages.inbounds.form.mtgRouteOutboundHint')}
        >
          <Select
            id="mtprotoOutboundTag"
            allowClear
            showSearch
            placeholder={t('pages.inbounds.form.mtgRouteOutboundPlaceholder')}
            options={(outboundTags ?? []).map((tag) => ({ value: tag, label: tag }))}
          />
        </FormField>
      )}
      <FormField
        name={['settings', 'publicIpv4']}
        label={t('pages.inbounds.form.mtgPublicIpv4')}
        tooltip={t('pages.inbounds.form.mtgPublicIpHint')}
      >
        <Input allowClear placeholder="1.2.3.4" />
      </FormField>
      <FormField
        name={['settings', 'publicIpv6']}
        label={t('pages.inbounds.form.mtgPublicIpv6')}
        tooltip={t('pages.inbounds.form.mtgPublicIpHint')}
      >
        <Input allowClear placeholder="2001:db8::1" />
      </FormField>
      <FormField
        name={['settings', 'concurrency']}
        label={t('pages.inbounds.form.mtgConcurrency')}
        tooltip={t('pages.inbounds.form.mtgConcurrencyHint')}
      >
        <InputNumber min={0} placeholder="8192" style={{ width: '100%' }} />
      </FormField>
      <FormField
        name={['settings', 'tolerateTimeSkewness']}
        label={t('pages.inbounds.form.mtgTimeSkew')}
        tooltip={t('pages.inbounds.form.mtgTimeSkewHint')}
      >
        <Input allowClear placeholder="3s" />
      </FormField>
      <FormField
        name={['settings', 'dns']}
        label={t('pages.inbounds.form.mtgDns')}
        tooltip={t('pages.inbounds.form.mtgDnsHint')}
      >
        <Input allowClear placeholder="https://1.1.1.1" />
      </FormField>
      <FormField
        name={['settings', 'doppelganger', 'urls']}
        label={t('pages.inbounds.form.mtgDoppelUrls')}
        tooltip={t('pages.inbounds.form.mtgDoppelUrlsHint')}
      >
        <UrlLines placeholder="https://www.cloudflare.com/" />
      </FormField>
      <Space style={{ marginBottom: 16 }}>
        <Button
          icon={<ReloadOutlined aria-hidden />}
          aria-label={t('pages.inbounds.form.mtgDoppelUrls')}
          onClick={() => {
            const domain = String(getValues('settings.fakeTlsDomain') || '').trim();
            if (domain)
              setValue('settings.doppelganger.urls', [`https://${domain}/`], { shouldDirty: true });
          }}
        />
      </Space>
      <FormField
        name={['settings', 'doppelganger', 'repeatsPerRaid']}
        label={t('pages.inbounds.form.mtgDoppelRepeats')}
      >
        <InputNumber min={0} placeholder="10" style={{ width: '100%' }} />
      </FormField>
      <FormField
        name={['settings', 'doppelganger', 'raidEach']}
        label={t('pages.inbounds.form.mtgDoppelRaidEach')}
      >
        <Input allowClear placeholder="6h" />
      </FormField>
      <FormField
        name={['settings', 'doppelganger', 'drs']}
        label={t('pages.inbounds.form.mtgDoppelDrs')}
        tooltip={t('pages.inbounds.form.mtgDoppelDrsHint')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      <FormField
        name={['settings', 'antiReplay', 'enabled']}
        label={t('pages.inbounds.form.mtgAntiReplay')}
        tooltip={t('pages.inbounds.form.mtgAntiReplayHint')}
      >
        <DefaultOnSwitch />
      </FormField>
      <FormField
        name={['settings', 'blocklist', 'enabled']}
        label={t('pages.inbounds.form.mtgBlocklist')}
        tooltip={t('pages.inbounds.form.mtgBlocklistHint')}
      >
        <DefaultOnSwitch />
      </FormField>
      <FormField
        name={['settings', 'blocklist', 'urls']}
        label={t('pages.inbounds.form.mtgBlocklistUrls')}
      >
        <UrlLines placeholder="https://iplists.firehol.org/files/firehol_level1.netset" />
      </FormField>
    </>
  );
}

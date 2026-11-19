import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Badge,
  Button,
  Card,
  Col,
  Collapse,
  ConfigProvider,
  Form,
  Layout,
  Result,
  Row,
  Space,
  Spin,
  message,
} from 'antd';
import { FormProvider, useForm, useWatch } from 'react-hook-form';

import AppSidebar from '@/layouts/AppSidebar';
import { useTheme } from '@/hooks/useTheme';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { getMessage, setMessageInstance } from '@/utils/messageBus';
import {
  fetchStubSite,
  useNginxCertificate,
  useNginxMutations,
  useNginxSettings,
  useNginxStatus,
  useNginxStubs,
} from '@/api/queries/useNginx';
import { formatCountdown, nginxWarningText, panelAddressAfterApply } from '@/lib/nginx/format';
import type {
  NginxPlan,
  NginxSettings,
  NginxStatus,
  StubCard,
  StubFormValues,
} from '@/schemas/api/nginx';
import {
  DomainSection,
  FirewallSection,
  ModeSection,
  PublishSection,
  type CertificateState,
} from './SettingsSections';
import RoutesTable from './RoutesTable';
import StubGallery from './StubGallery';
import StubEditorModal, { type StubDraft } from './StubEditorModal';
import PlanModal from './PlanModal';
import ApplyResultModal, { type ApplyResult } from './ApplyResultModal';
import './NginxPage.css';

// Mirrors ConfirmWindow in nginx_confirm.go, for when the status cannot be re-read after
// an apply that cut the connection it came over.
const CONFIRM_WINDOW_MS = 2 * 60 * 1000;

function useDebounced<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const handle = window.setTimeout(() => setDebounced(value), delayMs);
    return () => window.clearTimeout(handle);
  }, [value, delayMs]);
  return debounced;
}

// Ticks only while something is pending, so a page left open on an ordinary day does nothing.
function useSecondsLeft(deadline: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!deadline) return;
    const tick = () => setNow(Date.now());
    const first = window.setTimeout(tick, 0);
    const timer = window.setInterval(tick, 1000);
    return () => {
      window.clearTimeout(first);
      window.clearInterval(timer);
    };
  }, [deadline]);
  return deadline ? Math.max(0, Math.round((deadline - now) / 1000)) : 0;
}

export default function NginxPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const { isMobile } = useMediaQuery();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const statusQuery = useNginxStatus({ pollWhilePending: true });
  const settingsQuery = useNginxSettings();
  const loaded = statusQuery.data !== undefined && settingsQuery.data !== undefined;
  const failure = statusQuery.error ?? settingsQuery.error;

  const pageClass = ['settings-page', isDark && 'is-dark', isUltra && 'is-ultra']
    .filter(Boolean)
    .join(' ');

  let body;
  if (loaded) {
    body = (
      <NginxWorkspace
        status={statusQuery.data}
        statusUpdatedAt={statusQuery.dataUpdatedAt}
        settings={settingsQuery.data}
        isMobile={isMobile}
      />
    );
  } else if (failure) {
    body = (
      <Result
        status="error"
        title={t('somethingWentWrong')}
        subTitle={failure.message}
        extra={
          <Button
            type="primary"
            loading={statusQuery.isFetching || settingsQuery.isFetching}
            onClick={() => {
              void statusQuery.refetch();
              void settingsQuery.refetch();
            }}
          >
            {t('refresh')}
          </Button>
        }
      />
    );
  } else {
    body = <div className="loading-spacer" />;
  }

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      <Layout className={pageClass}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Spin
              aria-live="off"
              spinning={!loaded && !failure}
              delay={200}
              description={t('loading')}
              size="large"
            >
              {body}
            </Spin>
          </Layout.Content>
        </Layout>
      </Layout>
    </ConfigProvider>
  );
}

interface NginxWorkspaceProps {
  status: NginxStatus;
  statusUpdatedAt: number;
  settings: NginxSettings;
  isMobile: boolean;
}

function NginxWorkspace({ status, statusUpdatedAt, settings, isMobile }: NginxWorkspaceProps) {
  const { t } = useTranslation();
  const methods = useForm<NginxSettings>({ defaultValues: settings });
  useEffect(() => {
    methods.reset(settings);
  }, [settings, methods]);

  const mutations = useNginxMutations();
  const { data: stubs = [] } = useNginxStubs();

  const mode = useWatch({ control: methods.control, name: 'mode' });
  const domain = useWatch({ control: methods.control, name: 'domain' }).trim();
  // The certificate is checked for the domain on screen, not the one that is saved.
  const certDomain = useDebounced(domain, 400);
  const certQuery = useNginxCertificate(certDomain);
  const cert: CertificateState = {
    checking: domain !== certDomain || certQuery.isFetching,
    certOk: certQuery.data?.certOk ?? false,
    certFile: certQuery.data?.certFile ?? '',
    certExpiry: certQuery.data?.certExpiry ?? 0,
  };

  // Offering a mode the server cannot deliver only produces a confusing failure later.
  const canEnable = status.installed && status.hasStream;

  const [planned, setPlanned] = useState<{ plan: NginxPlan; settings: NginxSettings } | null>(null);
  const [applyResult, setApplyResult] = useState<ApplyResult | null>(null);
  const [assumed, setAssumed] = useState({ at: 0, deadline: 0 });
  const deadline =
    statusUpdatedAt > assumed.at
      ? status.confirmDeadline
      : assumed.deadline || status.confirmDeadline;
  const secondsLeft = useSecondsLeft(deadline);

  async function openPlan(values: NginxSettings) {
    const plan = await mutations.plan(values);
    if (plan) setPlanned({ plan, settings: values });
  }

  async function applyPlanned() {
    if (!planned) return;
    const change = panelAddressAfterApply(
      planned.settings,
      status.panelPort,
      window.X_UI_BASE_PATH || '/',
      window.location,
    );
    const outcome = await mutations.apply(planned.settings);
    if (outcome === 'refused') return;
    if (outcome === 'unanswered' && !change.disrupts) {
      getMessage().error(t('somethingWentWrong'));
      return;
    }
    setPlanned(null);
    if (change.disrupts) {
      if (!change.leaving443) {
        setAssumed({ at: Date.now(), deadline: Date.now() + CONFIRM_WINDOW_MS });
      }
      setApplyResult({ url: change.url, leaving443: change.leaving443 });
    }
    if (!change.leaving443) void mutations.invalidateAll();
  }

  const [editor, setEditor] = useState<{ key: number; draft: StubDraft } | null>(null);
  const [editorOpen, setEditorOpen] = useState(false);
  const [previewVersion, setPreviewVersion] = useState(0);

  function openEditor(draft: StubDraft) {
    setEditor((prev) => ({ key: (prev?.key ?? 0) + 1, draft }));
    setEditorOpen(true);
  }

  async function editStub(card: StubCard) {
    // A built-in page nobody has saved yet opens as a new page with the template in it.
    if (!card.id) {
      openEditor({ id: 0, name: card.name, html: card.html ?? '', template: card.key });
      return;
    }
    const site = await fetchStubSite(card.id);
    if (site) openEditor({ id: site.id, name: site.name, html: site.html });
  }

  async function saveStub(values: StubFormValues) {
    const result = await mutations.saveStub(values);
    if (!result) return null;
    setPreviewVersion((v) => v + 1);
    return { siteId: result.site.id, warnings: result.warnings };
  }

  const statusBadges = (
    <Space wrap>
      {status.installed ? (
        <Badge status="success" text={`nginx ${status.version}`} />
      ) : (
        <Badge status="error" text={t('pages.nginx.notInstalled')} />
      )}
      {status.installed &&
        (status.running ? (
          <Badge status="processing" text={t('pages.nginx.running')} />
        ) : (
          <Badge status="default" text={t('pages.nginx.stopped')} />
        ))}
      {status.installed &&
        (status.hasStream ? (
          <Badge status="success" text={t('pages.nginx.streamOk')} />
        ) : (
          <Badge status="error" text={t('pages.nginx.streamMissing')} />
        ))}
      {status.firewallOn ? (
        <Badge status="success" text={t('pages.nginx.firewallStateOn')} />
      ) : (
        status.mode === 'only443' && (
          <Badge status="warning" text={t('pages.nginx.firewallStateOff')} />
        )
      )}
    </Space>
  );

  const sections = [
    { key: 'mode', label: t('pages.nginx.mode'), children: <ModeSection canEnable={canEnable} /> },
    { key: 'domain', label: t('pages.nginx.domain'), children: <DomainSection cert={cert} /> },
    {
      key: 'publish',
      label: t('pages.nginx.publish'),
      children: <PublishSection certOk={cert.certOk} />,
    },
    { key: 'firewall', label: t('pages.nginx.firewall'), children: <FirewallSection /> },
    {
      key: 'stubs',
      label: t('pages.nginx.stubs'),
      children: (
        <StubGallery
          cards={stubs}
          previewVersion={previewVersion}
          onChoose={(card) => void mutations.activateStub(card)}
          onEdit={(card) => void editStub(card)}
          onDelete={(card) => void mutations.deleteStub(card.id)}
          onNew={() => openEditor({ id: 0, name: '', html: '' })}
        />
      ),
    },
    {
      key: 'routes',
      label: t('pages.nginx.routes'),
      children: <RoutesTable routes={status.routes} isMobile={isMobile} />,
    },
  ];

  return (
    <FormProvider {...methods}>
      <Row gutter={[isMobile ? 8 : 16, isMobile ? 8 : 12]}>
        <Col span={24}>
          <Card hoverable>
            <Row className="header-row" gutter={[16, 16]}>
              <Col xs={24} sm={6} className="header-actions">
                <Button
                  type="primary"
                  loading={mutations.planning}
                  disabled={mode !== 'off' && !canEnable}
                  onClick={methods.handleSubmit(openPlan)}
                >
                  {t('pages.nginx.apply')}
                </Button>
              </Col>
              <Col xs={24} sm={18} className="header-info">
                <Alert type="warning" title={statusBadges} />
              </Col>
            </Row>
          </Card>
        </Col>

        {/* Above the warnings: everything else on this page can wait two minutes, this cannot. */}
        {deadline > 0 && (
          <Col span={24}>
            <Alert
              type="error"
              showIcon
              title={t('pages.nginx.confirmPendingTitle')}
              description={
                <>
                  <p>
                    {t('pages.nginx.confirmPendingBody', {
                      time:
                        secondsLeft > 0
                          ? formatCountdown(secondsLeft)
                          : t('pages.nginx.confirmPendingExpired'),
                    })}
                  </p>
                  <Button
                    type="primary"
                    loading={mutations.confirming}
                    onClick={() => void mutations.confirm()}
                  >
                    {t('pages.nginx.confirmPendingButton')}
                  </Button>
                </>
              }
            />
          </Col>
        )}

        {status.warnings.map((warning, i) => (
          <Col span={24} key={i}>
            <Alert type="warning" showIcon title={nginxWarningText(warning, t)} />
          </Col>
        ))}

        <Col span={24}>
          <Card styles={{ body: { padding: isMobile ? 8 : 32 } }}>
            <Form layout="vertical" component={false}>
              <Collapse defaultActiveKey={['mode']} items={sections} />
            </Form>
          </Card>
        </Col>
      </Row>

      <PlanModal
        plan={planned?.plan ?? null}
        applying={mutations.applying}
        onApply={() => void applyPlanned()}
        onCancel={() => setPlanned(null)}
      />
      <ApplyResultModal
        result={applyResult}
        secondsLeft={secondsLeft}
        onClose={() => setApplyResult(null)}
      />
      {editor && (
        <StubEditorModal
          key={editor.key}
          open={editorOpen}
          draft={editor.draft}
          isMobile={isMobile}
          saving={mutations.savingStub}
          onSave={saveStub}
          onClose={() => setEditorOpen(false)}
        />
      )}
    </FormProvider>
  );
}

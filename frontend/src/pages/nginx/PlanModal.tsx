import type { ComponentType } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Empty, Modal } from 'antd';
import {
  ForkOutlined,
  GlobalOutlined,
  LinkOutlined,
  ProfileOutlined,
  SafetyOutlined,
  StopOutlined,
  SwapOutlined,
} from '@ant-design/icons';

import { nginxChangeText, nginxWarningText } from '@/lib/nginx/format';
import type { NginxPlan } from '@/schemas/api/nginx';

const CHANGE_ICONS: Record<string, ComponentType<{ 'aria-hidden'?: boolean }>> = {
  links: LinkOutlined,
  subs: ProfileOutlined,
  closed: StopOutlined,
  kept: SafetyOutlined,
  dual: ForkOutlined,
  move: SwapOutlined,
};

const NOTE_STYLE = { marginTop: 12 };

interface PlanModalProps {
  plan: NginxPlan | null;
  applying: boolean;
  onApply: () => void;
  onCancel: () => void;
}

// Nothing moves until this has been read: a link that changes cannot be un-changed for
// the clients already holding it.
export default function PlanModal({ plan, applying, onApply, onCancel }: PlanModalProps) {
  const { t } = useTranslation();
  const changes = plan?.changes ?? [];
  const blockers = plan?.blockers ?? [];
  const has = (kind: string) => changes.some((change) => change.kind === kind);

  return (
    <Modal
      open={plan !== null}
      title={t('pages.nginx.planTitle')}
      okText={t('pages.nginx.confirm')}
      cancelText={t('cancel')}
      confirmLoading={applying}
      okButtonProps={{ disabled: blockers.length > 0 }}
      onOk={onApply}
      onCancel={onCancel}
    >
      {blockers.map((blocker, i) => (
        <Alert
          key={i}
          type="error"
          showIcon
          title={nginxWarningText(blocker, t)}
          style={{ marginBottom: 8 }}
        />
      ))}
      {blockers.length === 0 && (
        <>
          {changes.length === 0 ? (
            <Empty description={t('pages.nginx.planEmpty')} />
          ) : (
            changes.map((change, i) => {
              const Icon = CHANGE_ICONS[change.kind] ?? GlobalOutlined;
              return (
                <div key={i} className="nginx-plan-row">
                  <Icon aria-hidden />
                  <span>{nginxChangeText(change, t)}</span>
                </div>
              );
            })
          )}
          {has('dual') && (
            <Alert type="info" showIcon title={t('pages.nginx.dualNote')} style={NOTE_STYLE} />
          )}
          {has('links') && (
            <Alert
              type="warning"
              showIcon
              title={t('pages.nginx.linksWarning')}
              style={NOTE_STYLE}
            />
          )}
          {has('subs') && (
            <Alert
              type="warning"
              showIcon
              title={t('pages.nginx.subsWarning')}
              style={NOTE_STYLE}
            />
          )}
          {has('closed') && (
            <Alert
              type="error"
              showIcon
              title={t('pages.nginx.closedWarning')}
              style={NOTE_STYLE}
            />
          )}
        </>
      )}
    </Modal>
  );
}

import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Input, Modal, message } from 'antd';
import { CopyOutlined, ExportOutlined, LinkOutlined } from '@ant-design/icons';

import { ClipboardManager } from '@/utils';
import { formatCountdown } from '@/lib/nginx/format';

export interface ApplyResult {
  url: string;
  leaving443: boolean;
}

interface ApplyResultModalProps {
  result: ApplyResult | null;
  secondsLeft: number;
  onClose: () => void;
}

// Shown when the apply moved the panel or closed ports, so the operator is handed the
// new address instead of being left on a connection that may be about to die.
export default function ApplyResultModal({ result, secondsLeft, onClose }: ApplyResultModalProps) {
  const { t } = useTranslation();
  const [messageApi, messageContextHolder] = message.useMessage();
  const urlLabelId = useId();
  const url = result?.url ?? '';

  async function copyUrl() {
    if (url && (await ClipboardManager.copyText(url))) messageApi.success(t('copied'));
  }

  const remaining =
    secondsLeft > 0
      ? `${t('pages.nginx.confirmPendingTitle')} (${formatCountdown(secondsLeft)})`
      : t('pages.nginx.confirmPendingTitle');

  return (
    <Modal
      open={result !== null}
      title={t(
        result?.leaving443 ? 'pages.nginx.applyModalLeavingTitle' : 'pages.nginx.applyModalTitle',
      )}
      mask={{ closable: false }}
      onCancel={onClose}
      footer={[
        <Button key="copy" icon={<CopyOutlined aria-hidden />} onClick={copyUrl}>
          {t('pages.nginx.applyModalCopyButton')}
        </Button>,
        <Button
          key="open"
          type="primary"
          icon={<ExportOutlined aria-hidden />}
          onClick={() => window.location.assign(url)}
        >
          {t('pages.nginx.applyModalOpenButton')}
        </Button>,
      ]}
    >
      {messageContextHolder}
      {result?.leaving443 ? (
        <Alert
          type="info"
          showIcon
          title={t('pages.nginx.applyModalLeavingNotice')}
          description={t('pages.nginx.applyModalLeavingBody')}
          style={{ marginBottom: 16 }}
        />
      ) : (
        <Alert
          type="warning"
          showIcon
          title={remaining}
          description={t('pages.nginx.applyModalBody')}
          style={{ marginBottom: 16 }}
        />
      )}
      <div id={urlLabelId} style={{ marginBottom: 8, fontWeight: 'bold' }}>
        {t('pages.nginx.applyModalUrlLabel')}
      </div>
      <Input
        aria-labelledby={urlLabelId}
        value={url}
        readOnly
        prefix={<LinkOutlined aria-hidden />}
      />
    </Modal>
  );
}

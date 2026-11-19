import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Form, Input, Modal, Select, Space, Tabs, Upload } from 'antd';
import { UploadOutlined } from '@ant-design/icons';
import { FormProvider, useForm, useWatch } from 'react-hook-form';

import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { SettingListItem } from '@/components/ui';
import { useNginxStubTemplates } from '@/api/queries/useNginx';
import { nginxWarningText } from '@/lib/nginx/format';
import { StubFormSchema, type NginxWarning, type StubFormValues } from '@/schemas/api/nginx';

export interface StubDraft extends StubFormValues {
  template?: string;
}

interface StubEditorModalProps {
  open: boolean;
  draft: StubDraft;
  isMobile: boolean;
  saving: boolean;
  onSave: (values: StubFormValues) => Promise<{ siteId: number; warnings: NginxWarning[] } | null>;
  onClose: () => void;
}

export default function StubEditorModal({
  open,
  draft,
  isMobile,
  saving,
  onSave,
  onClose,
}: StubEditorModalProps) {
  const { t } = useTranslation();
  // The page remounts this per opening (key), so the draft only seeds initial state.
  const methods = useForm<StubFormValues>({ defaultValues: draft });
  const { data: templates = [] } = useNginxStubTemplates(open);
  const [template, setTemplate] = useState<string | undefined>(draft.template);
  const [warnings, setWarnings] = useState<NginxWarning[]>([]);
  const id = useWatch({ control: methods.control, name: 'id' });
  const html = useWatch({ control: methods.control, name: 'html' });

  function fillFrom(content: string, nameGuess: string) {
    methods.setValue('html', content, { shouldDirty: true, shouldValidate: true });
    if (!methods.getValues('name').trim()) {
      methods.setValue('name', nameGuess, { shouldDirty: true, shouldValidate: true });
    }
  }

  function onTemplateChange(key: string) {
    setTemplate(key);
    const tpl = templates.find((x) => x.key === key);
    if (tpl) fillFrom(tpl.html, tpl.name);
  }

  async function onFinish(values: StubFormValues) {
    const result = await onSave(values);
    if (!result) return;
    setWarnings(result.warnings);
    // Warnings are advice, not a failure: the editor stays open so they can be acted on.
    if (result.warnings.length === 0) onClose();
    else methods.setValue('id', result.siteId);
  }

  return (
    <Modal
      open={open}
      title={t(id ? 'pages.nginx.stubEdit' : 'pages.nginx.stubNew')}
      width={isMobile ? '100%' : 900}
      okText={t('save')}
      cancelText={t('cancel')}
      confirmLoading={saving}
      mask={{ closable: false }}
      onOk={methods.handleSubmit(onFinish)}
      onCancel={onClose}
    >
      <FormProvider {...methods}>
        <Form layout="vertical" component={false}>
          <SettingListItem
            paddings="small"
            title={t('pages.nginx.stubName')}
            description={t('pages.nginx.stubNameDesc')}
          >
            <FormField
              name="name"
              style={{ marginBottom: 0 }}
              rules={{ validate: rhfZodValidate(StubFormSchema.shape.name) }}
            >
              <Input aria-label={t('pages.nginx.stubName')} placeholder="example.com" />
            </FormField>
          </SettingListItem>
          <SettingListItem
            paddings="small"
            title={t('pages.nginx.stubStartFrom')}
            description={t('pages.nginx.stubStartFromDesc')}
          >
            <Space wrap>
              <Select
                aria-label={t('pages.nginx.stubTemplate')}
                style={{ minWidth: 190 }}
                value={template}
                placeholder={t('pages.nginx.stubTemplate')}
                onChange={onTemplateChange}
                options={templates.map((tpl) => ({ value: tpl.key, label: tpl.name }))}
              />
              <Upload
                accept=".html,.htm"
                showUploadList={false}
                beforeUpload={(file) => {
                  void file
                    .text()
                    .then((content) => fillFrom(content, file.name.replace(/\.html?$/i, '')));
                  return Upload.LIST_IGNORE;
                }}
              >
                <Button icon={<UploadOutlined aria-hidden />}>{t('pages.nginx.stubUpload')}</Button>
              </Upload>
            </Space>
          </SettingListItem>

          <Tabs
            defaultActiveKey="code"
            style={{ marginTop: 8 }}
            items={[
              {
                key: 'code',
                label: t('pages.nginx.stubCode'),
                children: (
                  <FormField
                    name="html"
                    rules={{ validate: rhfZodValidate(StubFormSchema.shape.html) }}
                  >
                    <Input.TextArea
                      aria-label={t('pages.nginx.stubCode')}
                      autoSize={{ minRows: 12, maxRows: 20 }}
                      spellCheck={false}
                      style={{
                        fontFamily:
                          'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
                        fontSize: 13,
                      }}
                    />
                  </FormField>
                ),
              },
              {
                key: 'preview',
                label: t('pages.nginx.stubPreview'),
                children: (
                  <iframe
                    className="stub-editor-preview"
                    title={t('pages.nginx.stubPreview')}
                    srcDoc={html}
                    sandbox=""
                  />
                ),
              },
            ]}
          />
        </Form>
      </FormProvider>

      {warnings.map((warning, i) => (
        <Alert
          key={i}
          type="warning"
          showIcon
          title={nginxWarningText(warning, t)}
          style={{ marginTop: 12 }}
        />
      ))}
    </Modal>
  );
}

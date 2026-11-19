import { useTranslation } from 'react-i18next';
import { Alert, Empty, Table, Tag, type TableColumnType } from 'antd';

import type { NginxRoute } from '@/schemas/api/nginx';

export default function RoutesTable({
  routes,
  isMobile,
}: {
  routes: NginxRoute[];
  isMobile: boolean;
}) {
  const { t } = useTranslation();
  const columns: TableColumnType<NginxRoute>[] = [
    { title: t('pages.nginx.routeInbound'), dataIndex: 'remark', key: 'remark' },
    { title: t('pages.nginx.routeProtocol'), dataIndex: 'protocol', key: 'protocol' },
    {
      title: t('pages.nginx.routeSni'),
      key: 'sni',
      render: (_, route) => (
        <>
          {route.snis.map((sni) => (
            <Tag key={sni}>{sni}</Tag>
          ))}
          {route.fallback && <Tag color="blue">{t('pages.nginx.routeFallback')}</Tag>}
        </>
      ),
    },
    {
      title: t('pages.nginx.routeInternal'),
      key: 'internal',
      render: (_, route) => (
        <Tag color="purple">
          {route.listen || '0.0.0.0'}:{route.port}
        </Tag>
      ),
    },
  ];

  return (
    <>
      <Alert
        type="info"
        showIcon
        title={t('pages.nginx.routesDesc')}
        style={{ marginBottom: 12 }}
      />
      {routes.length > 0 ? (
        <Table
          columns={columns}
          dataSource={routes}
          rowKey={(route) => route.inboundId}
          pagination={false}
          scroll={isMobile ? { x: 600 } : undefined}
          size="small"
        />
      ) : (
        <Empty description={t('pages.nginx.noRoutes')} />
      )}
    </>
  );
}

import { useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Tag, Tooltip, type TableColumnType } from 'antd';

import { useNginxStatus } from '@/api/queries/useNginx';
import { nginxPortRoute } from '@/lib/nginx/format';
import type { DBInboundRecord } from './types';

// 3AX-UI: an inbound behind nginx listens on the loopback but is published on 443;
// showing only one of the two numbers reads as a bug.
function useNginxPortTag() {
  const { t } = useTranslation();
  const { data: status } = useNginxStatus();
  const hasRoutes = !!status && status.mode !== 'off' && status.routes.length > 0;
  const render = useCallback(
    (inboundId: number) => {
      const route = status ? nginxPortRoute(status, inboundId) : null;
      if (!route) return null;
      const hint = t(route.dual ? 'pages.inbounds.portBoth' : 'pages.inbounds.portViaOnly', {
        public: route.publicPort,
        port: route.port,
      });
      return (
        <Tooltip title={hint}>
          <Tag color="blue" style={{ marginInlineEnd: 0 }}>
            {route.label}
          </Tag>
        </Tooltip>
      );
    },
    [status, t],
  );
  return { hasRoutes, render };
}

export function NginxPortTag({ inboundId, port }: { inboundId: number; port: number }) {
  const { render } = useNginxPortTag();
  return render(inboundId) ?? <Tag>{port}</Tag>;
}

export function useNginxPortColumn(): Partial<TableColumnType<DBInboundRecord>> | null {
  const { hasRoutes, render } = useNginxPortTag();
  return useMemo(
    () =>
      hasRoutes
        ? {
            width: 120,
            render: (_: unknown, record: DBInboundRecord) => render(record.id) ?? record.port,
          }
        : null,
    [hasRoutes, render],
  );
}

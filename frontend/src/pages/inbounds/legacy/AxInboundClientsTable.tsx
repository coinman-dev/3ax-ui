import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Dropdown, Modal, Space, Switch, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  DeleteOutlined,
  DisconnectOutlined,
  EditOutlined,
  InfoCircleOutlined,
  MoreOutlined,
  QrcodeOutlined,
  RetweetOutlined,
} from '@ant-design/icons';

import { useClients, type ClientRecord } from '@/hooks/useClients';
import { useDatepicker } from '@/hooks/useDatepicker';
import { useWebSocket } from '@/hooks/useWebSocket';
import ClientTrafficCell from '@/components/clients/ClientTrafficCell';
import ClientCardComment from '@/components/clients/ClientCardComment';
import { ClientRowActions } from '@/pages/clients/RowCells';
import { IntlUtil } from '@/utils';

import {
  axClientBucket,
  axDelayedDays,
  axExpiryColor,
  axNestedQuery,
  axRemaining,
  type AxClientModalRequest,
} from './axInboundClients';
import './AxInboundClients.css';

const sharedWithOthers = (record: ClientRecord) => (record.inboundIds?.length ?? 0) > 1;

interface AxInboundClientsTableProps {
  inboundId: number;
  // Only AmneziaWG clients have forwarded ports, so only its inbounds get 1.x's Mapping column.
  showForwardedPorts: boolean;
  onlineEmails: string[];
  isMobile: boolean;
  onOpen: (request: AxClientModalRequest) => void;
}

// 3AX-UI: the 1.x nested clients table of one inbound. Rows come from the paged clients API
// filtered to this inbound, so a client attached to two inbounds shows under both.
export default function AxInboundClientsTable({
  inboundId,
  showForwardedPorts,
  onlineEmails,
  isMobile,
  onOpen,
}: AxInboundClientsTableProps) {
  const { t } = useTranslation();
  const { datepicker } = useDatepicker();
  const [modal, modalContextHolder] = Modal.useModal();
  const {
    clients,
    filtered,
    fetched,
    transitioning,
    setQuery,
    pageSize,
    settingsReady,
    expireDiff,
    trafficDiff,
    hydrate,
    setEnable,
    resetTraffic,
    remove,
    detach,
    applyClientStatsEvent,
  } = useClients();
  const [page, setPage] = useState(1);
  const [togglingEmail, setTogglingEmail] = useState<string | null>(null);

  const query = useMemo(
    () => axNestedQuery(inboundId, page, pageSize),
    [inboundId, page, pageSize],
  );
  useEffect(() => {
    if (settingsReady) setQuery(query);
  }, [settingsReady, query, setQuery]);

  useWebSocket({ client_stats: applyClientStatsEvent });

  const lastPage = Math.max(1, Math.ceil(filtered / query.pageSize));
  if (fetched && !transitioning && page > lastPage) setPage(lastPage);

  const onlineSet = useMemo(() => new Set(onlineEmails), [onlineEmails]);
  const isOnline = useCallback((email: string) => onlineSet.has(email), [onlineSet]);

  // Handlers take an email and read the live row at call time, so ClientRowActions stays memoised.
  const rowsByEmail = useRef(new Map<string, ClientRecord>());
  const rowMap = useMemo(() => new Map(clients.map((c) => [c.email, c])), [clients]);
  useEffect(() => {
    rowsByEmail.current = rowMap;
  }, [rowMap]);

  const openHydrated = useCallback(
    async (email: string, kind: 'qr' | 'info' | 'edit') => {
      const row = rowsByEmail.current.get(email);
      if (!row) return;
      const full = await hydrate(row.email);
      const tunnelAllowedIPs = full?.tunnelAllowedIPs ?? {};
      if (kind === 'edit') {
        onOpen({
          kind,
          client: full ? { ...row, ...full.client } : { ...row },
          attachedIds: [...(full?.inboundIds ?? row.inboundIds ?? [])],
          externalLinks: Array.isArray(full?.externalLinks) ? [...full.externalLinks] : [],
          tunnelAllowedIPs,
        });
        return;
      }
      onOpen({
        kind,
        client: full ? { ...row, ...full.client, inboundIds: full.inboundIds } : row,
        tunnelAllowedIPs,
        online: !!row.enable && onlineSet.has(row.email),
      });
    },
    [hydrate, onOpen, onlineSet],
  );

  const onShowQr = useCallback((email: string) => void openHydrated(email, 'qr'), [openHydrated]);
  const onShowInfo = useCallback(
    (email: string) => void openHydrated(email, 'info'),
    [openHydrated],
  );
  const onEdit = useCallback((email: string) => void openHydrated(email, 'edit'), [openHydrated]);

  const onResetTraffic = useCallback(
    (email: string) => {
      const row = rowsByEmail.current.get(email);
      if (!row) return;
      modal.confirm({
        title: `${t('pages.inbounds.resetTraffic')} — ${row.email}`,
        content: t('pages.inbounds.resetTrafficContent'),
        okText: t('reset'),
        cancelText: t('cancel'),
        onOk: () => resetTraffic(row),
      });
    },
    [modal, t, resetTraffic],
  );

  const onDelete = useCallback(
    (email: string) => {
      const row = rowsByEmail.current.get(email);
      if (!row) return;
      modal.confirm({
        title: t('pages.clients.deleteConfirmTitle', { email: row.email }),
        content: t('pages.clients.deleteConfirmContent'),
        okText: t('delete'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: () => remove(row.email),
      });
    },
    [modal, t, remove],
  );

  // 1.x took a client off one inbound; with several inbounds that is not the same as deleting it.
  const onRemoveFromInbound = useCallback(
    (email: string) => {
      modal.confirm({
        title: t('pages.inbounds.axRemoveFromInbound'),
        content: t('pages.inbounds.axRemoveFromInboundContent', { email }),
        okText: t('pages.inbounds.axRemove'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: () => detach(email, [inboundId]),
      });
    },
    [modal, t, detach, inboundId],
  );

  const onToggleEnable = useCallback(
    async (row: ClientRecord, next: boolean) => {
      setTogglingEmail(row.email);
      try {
        await setEnable(row, next);
      } finally {
        setTogglingEmail(null);
      }
    },
    [setEnable],
  );

  const columns = useMemo<ColumnsType<ClientRecord>>(() => {
    const statusTag = (record: ClientRecord) => {
      const bucket = axClientBucket(record, Date.now(), expireDiff, trafficDiff);
      const lastOnline = record.traffic?.lastOnline ?? 0;
      const lastOnlineTitle = `${t('lastOnline')}: ${lastOnline > 0 ? IntlUtil.formatDate(lastOnline, datepicker) : '-'}`;
      if (bucket === 'depleted')
        return (
          <Tooltip title={lastOnlineTitle}>
            <Tag color="red">{t('depleted')}</Tag>
          </Tooltip>
        );
      if (record.enable && isOnline(record.email))
        return (
          <Tag color="green" className="dot-tag">
            <span className="online-dot" />
            {t('pages.clients.online')}
          </Tag>
        );
      if (!record.enable) return <Tag>{t('disabled')}</Tag>;
      if (bucket === 'expiring') return <Tag color="orange">{t('depletingSoon')}</Tag>;
      return (
        <Tooltip title={lastOnlineTitle}>
          <Tag>{t('pages.clients.offline')}</Tag>
        </Tooltip>
      );
    };
    const expiryTag = (record: ClientRecord) => {
      const expiry = record.expiryTime ?? 0;
      const delayed = expiry < 0 ? `${axDelayedDays(expiry)}d` : '';
      const full = !expiry
        ? '∞'
        : delayed
          ? `${t('pages.clients.delayedStart')}: ${delayed}`
          : IntlUtil.formatDate(expiry, datepicker);
      return (
        <Tooltip title={full}>
          <Tag color={axExpiryColor(expiry, Date.now())}>
            {!expiry ? '∞' : delayed || IntlUtil.formatRelativeTime(expiry)}
          </Tag>
        </Tooltip>
      );
    };
    const enableSwitch = (record: ClientRecord) => (
      <Switch
        checked={!!record.enable}
        size="small"
        loading={togglingEmail === record.email}
        aria-label={t('pages.clients.enabled')}
        onChange={(next) => void onToggleEnable(record, next)}
      />
    );
    const emailCell = (record: ClientRecord) => (
      <div className="ax-client-email-cell">
        <span className="ax-client-email">{record.email}</span>
        <ClientCardComment comment={record.comment} className="ax-client-comment" />
      </div>
    );
    const portsTag = (record: ClientRecord) => {
      const ports = record.forwardedPorts?.trim();
      return ports ? (
        <Tag color="cyan" className="ax-client-ports">
          {ports}
        </Tag>
      ) : (
        <Tag className="ax-client-ports-none">—</Tag>
      );
    };
    const trafficCell = (record: ClientRecord, compact: boolean) => (
      <ClientTrafficCell
        compact={compact}
        up={record.traffic?.up}
        down={record.traffic?.down}
        total={record.totalGB}
        enabled={record.enable}
        trafficDiff={trafficDiff}
      />
    );

    if (isMobile) {
      return [
        {
          title: t('pages.clients.client'),
          key: 'email',
          render: (_v, record) => (
            <div className="ax-client-mobile-cell">
              {emailCell(record)}
              <div className="ax-client-mobile-tags">
                {statusTag(record)}
                {expiryTag(record)}
                {showForwardedPorts && record.forwardedPorts?.trim() && portsTag(record)}
              </div>
              {trafficCell(record, true)}
            </div>
          ),
        },
        {
          key: 'actions',
          width: 90,
          align: 'center',
          render: (_v, record) => (
            <div className="ax-client-mobile-actions">
              {enableSwitch(record)}
              <Dropdown
                trigger={['click']}
                placement="bottomRight"
                menu={{
                  items: [
                    {
                      key: 'qr',
                      icon: <QrcodeOutlined aria-hidden />,
                      label: t('pages.clients.qrCode'),
                      onClick: () => onShowQr(record.email),
                    },
                    {
                      key: 'info',
                      icon: <InfoCircleOutlined aria-hidden />,
                      label: t('pages.clients.clientInfo'),
                      onClick: () => onShowInfo(record.email),
                    },
                    {
                      key: 'reset',
                      icon: <RetweetOutlined aria-hidden />,
                      label: t('pages.inbounds.resetTraffic'),
                      onClick: () => onResetTraffic(record.email),
                    },
                    {
                      key: 'edit',
                      icon: <EditOutlined aria-hidden />,
                      label: t('edit'),
                      onClick: () => onEdit(record.email),
                    },
                    ...(sharedWithOthers(record)
                      ? [
                          {
                            key: 'removeFromInbound',
                            icon: <DisconnectOutlined aria-hidden />,
                            label: t('pages.inbounds.axRemoveFromInbound'),
                            onClick: () => onRemoveFromInbound(record.email),
                          },
                        ]
                      : []),
                    {
                      key: 'delete',
                      danger: true,
                      icon: <DeleteOutlined aria-hidden />,
                      label: t('delete'),
                      onClick: () => onDelete(record.email),
                    },
                  ],
                }}
              >
                <Button
                  type="text"
                  size="small"
                  icon={<MoreOutlined aria-hidden />}
                  aria-label={t('more')}
                />
              </Dropdown>
            </div>
          ),
        },
      ];
    }

    return [
      {
        title: t('pages.clients.actions'),
        key: 'actions',
        width: 220,
        render: (_v, record) => (
          <Space size={4}>
            <ClientRowActions
              email={record.email}
              onShowQr={onShowQr}
              onShowInfo={onShowInfo}
              onResetTraffic={onResetTraffic}
              onEdit={onEdit}
              onDelete={onDelete}
            />
            {sharedWithOthers(record) && (
              <Tooltip title={t('pages.inbounds.axRemoveFromInbound')}>
                <Button
                  size="small"
                  type="text"
                  icon={<DisconnectOutlined aria-hidden />}
                  aria-label={t('pages.inbounds.axRemoveFromInbound')}
                  onClick={() => onRemoveFromInbound(record.email)}
                />
              </Tooltip>
            )}
          </Space>
        ),
      },
      {
        title: t('pages.clients.enabled'),
        key: 'enable',
        width: 80,
        render: (_v, record) => enableSwitch(record),
      },
      {
        title: t('pages.clients.online'),
        key: 'online',
        width: 100,
        render: (_v, record) => statusTag(record),
      },
      {
        title: t('pages.clients.client'),
        key: 'email',
        width: 220,
        render: (_v, record) => emailCell(record),
      },
      ...(showForwardedPorts
        ? [
            {
              title: t('pages.inbounds.axForwardedPortsShort'),
              key: 'forwardedPorts',
              width: 120,
              align: 'center' as const,
              render: (_v: unknown, record: ClientRecord) => portsTag(record),
            },
          ]
        : []),
      {
        title: t('pages.clients.traffic'),
        key: 'traffic',
        width: 200,
        render: (_v, record) => trafficCell(record, false),
      },
      {
        title: t('pages.clients.remaining'),
        key: 'remaining',
        width: 110,
        render: (_v, record) => {
          const remaining = axRemaining(record);
          return <Tag color={remaining.color}>{remaining.label}</Tag>;
        },
      },
      {
        title: t('pages.clients.duration'),
        key: 'expiryTime',
        width: 120,
        render: (_v, record) => expiryTag(record),
      },
    ];
  }, [
    t,
    isMobile,
    showForwardedPorts,
    datepicker,
    expireDiff,
    trafficDiff,
    isOnline,
    togglingEmail,
    onToggleEnable,
    onShowQr,
    onShowInfo,
    onResetTraffic,
    onEdit,
    onDelete,
    onRemoveFromInbound,
  ]);

  return (
    <div className="ax-inbound-clients">
      {modalContextHolder}
      <Table<ClientRecord>
        size="small"
        rowKey={(r) => r.email}
        columns={columns}
        dataSource={clients}
        loading={!fetched}
        showHeader={!isMobile}
        scroll={isMobile ? undefined : { x: 1100 }}
        pagination={{
          current: page,
          pageSize: query.pageSize,
          total: filtered,
          showSizeChanger: false,
          hideOnSinglePage: true,
          size: 'small',
          onChange: setPage,
        }}
      />
    </div>
  );
}

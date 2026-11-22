import { lazy, useCallback, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, type MenuProps, type TableProps } from 'antd';
import { DownOutlined, UpOutlined, UserAddOutlined, UsergroupAddOutlined } from '@ant-design/icons';

import { LazyMount } from '@/components/utility';
import { useLegacyInboundsView } from '@/hooks/useLegacyInboundsView';
import type { HostRecord } from '@/schemas/api/host';

import { isInboundMultiUser } from '../list/helpers';
import type { ClientCountEntry, DBInboundRecord } from '../list/types';
import AxInboundClientsTable from './AxInboundClientsTable';
import type { AxClientModalRequest } from './axInboundClients';

const AxClientModals = lazy(() => import('./AxClientModals'));

const ADD_CLIENT_KEY = 'axAddClient';
const ADD_BULK_CLIENTS_KEY = 'axAddBulkClients';
const NO_EMAILS: string[] = [];

interface UseAxLegacyInboundsParams {
  clientCount: Record<number, ClientCountEntry>;
  hosts: HostRecord[];
  isMobile: boolean;
}

export interface AxLegacyInbounds {
  expandable?: TableProps<DBInboundRecord>['expandable'];
  extraRowActions?: (record: DBInboundRecord) => MenuProps['items'];
  renderCardExtra?: (record: DBInboundRecord) => ReactNode;
  handleRowAction: (key: string, inboundId: number) => boolean;
  modals: ReactNode;
}

function AxCardClients({ count, children }: { count: number; children: ReactNode }) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  return (
    <div className="ax-card-clients">
      <Button
        type="link"
        size="small"
        icon={expanded ? <UpOutlined aria-hidden /> : <DownOutlined aria-hidden />}
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
      >
        {`${t('menu.clients')} (${count})`}
      </Button>
      {expanded && children}
    </div>
  );
}

// 3AX-UI: everything the Inbounds page needs for the 1.x layout; inert while the setting is off.
export function useAxLegacyInbounds({
  clientCount,
  hosts,
  isMobile,
}: UseAxLegacyInboundsParams): AxLegacyInbounds {
  const { t } = useTranslation();
  const enabled = useLegacyInboundsView();
  const [request, setRequest] = useState<AxClientModalRequest | null>(null);
  const [modalOpen, setModalOpen] = useState(false);

  const openModal = useCallback((next: AxClientModalRequest) => {
    setRequest(next);
    setModalOpen(true);
  }, []);
  const closeModal = useCallback(() => setModalOpen(false), []);

  const renderClients = useCallback(
    (record: DBInboundRecord) => (
      <AxInboundClientsTable
        inboundId={record.id}
        showForwardedPorts={!!record.isAmneziawg}
        onlineEmails={clientCount[record.id]?.online ?? NO_EMAILS}
        isMobile={isMobile}
        onOpen={openModal}
      />
    ),
    [clientCount, isMobile, openModal],
  );

  const expandable = useMemo<AxLegacyInbounds['expandable']>(
    () =>
      enabled
        ? {
            rowExpandable: (record) => isInboundMultiUser(record),
            // rc-table keeps a collapsed row mounted; rendering nothing stops its 5 s poll.
            expandedRowRender: (record, _index, _indent, expanded) =>
              expanded ? renderClients(record) : null,
          }
        : undefined,
    [enabled, renderClients],
  );

  const extraRowActions = useMemo<AxLegacyInbounds['extraRowActions']>(
    () =>
      enabled
        ? (record) =>
            isInboundMultiUser(record)
              ? [
                  {
                    key: ADD_CLIENT_KEY,
                    icon: <UserAddOutlined aria-hidden />,
                    label: t('pages.clients.addClient'),
                  },
                  {
                    key: ADD_BULK_CLIENTS_KEY,
                    icon: <UsergroupAddOutlined aria-hidden />,
                    label: t('pages.clients.bulk'),
                  },
                  { type: 'divider' },
                ]
              : []
        : undefined,
    [enabled, t],
  );

  const renderCardExtra = useMemo<AxLegacyInbounds['renderCardExtra']>(
    () =>
      enabled
        ? (record) =>
            isInboundMultiUser(record) ? (
              <AxCardClients count={clientCount[record.id]?.clients ?? 0}>
                {renderClients(record)}
              </AxCardClients>
            ) : null
        : undefined,
    [enabled, clientCount, renderClients],
  );

  const handleRowAction = useCallback(
    (key: string, inboundId: number) => {
      if (key === ADD_CLIENT_KEY) openModal({ kind: 'add', inboundId });
      else if (key === ADD_BULK_CLIENTS_KEY) openModal({ kind: 'bulkAdd', inboundId });
      else return false;
      return true;
    },
    [openModal],
  );

  const modals = enabled && request && (
    <LazyMount when>
      <AxClientModals request={request} open={modalOpen} hosts={hosts} onClose={closeModal} />
    </LazyMount>
  );

  return { expandable, extraRowActions, renderCardExtra, handleRowAction, modals };
}

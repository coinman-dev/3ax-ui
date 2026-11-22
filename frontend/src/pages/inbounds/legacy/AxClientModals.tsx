import { lazy, useCallback, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';

import { keys } from '@/api/queryKeys';
import { LazyMount } from '@/components/utility';
import { useClients, type ExternalLinkInput, type InboundOption } from '@/hooks/useClients';
import { GroupSummaryListSchema, type GroupSummary } from '@/schemas/client';
import type { HostRecord } from '@/schemas/api/host';
import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';

import type { AxClientModalRequest } from './axInboundClients';

const ClientFormModal = lazy(() => import('@/pages/clients/ClientFormModal'));
const ClientInfoModal = lazy(() => import('@/pages/clients/ClientInfoModal'));
const ClientQrModal = lazy(() => import('@/pages/clients/ClientQrModal'));
const ClientBulkAddModal = lazy(() => import('@/pages/clients/ClientBulkAddModal'));

const NO_TUNNEL_IPS: Record<number, string> = {};

async function fetchGroups(): Promise<GroupSummary[]> {
  const msg = await HttpUtil.get('/panel/api/clients/groups', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to load groups');
  return parseMsg(msg, GroupSummaryListSchema, 'clients/groups').obj ?? [];
}

type SavePayload =
  | Record<string, unknown>
  | { client: Record<string, unknown>; inboundIds: number[] };
type SaveMeta =
  | { isEdit: false; email: string; externalLinks: ExternalLinkInput[] }
  | {
      isEdit: true;
      email: string;
      attach: number[];
      detach: number[];
      externalLinks: ExternalLinkInput[];
    };

interface AxClientModalsProps {
  request: AxClientModalRequest;
  open: boolean;
  hosts: HostRecord[];
  onClose: () => void;
}

// 3AX-UI: the Clients page's own client modals, opened from the 1.x inbounds page.
export default function AxClientModals({ request, open, hosts, onClose }: AxClientModalsProps) {
  const {
    inbounds,
    subSettings,
    tgBotEnable,
    create,
    update,
    attach,
    detach,
    setExternalLinks,
    resetTraffic,
  } = useClients();
  const groupsQuery = useQuery({
    queryKey: keys.clients.groups(),
    queryFn: fetchGroups,
    staleTime: 30_000,
  });
  const groups = useMemo(
    () => (groupsQuery.data ?? []).map((g) => g.name).filter(Boolean),
    [groupsQuery.data],
  );
  const inboundsById = useMemo(() => {
    const out: Record<number, InboundOption> = {};
    for (const ib of inbounds) out[ib.id] = ib;
    return out;
  }, [inbounds]);

  // The Clients page's save sequence: create or update, then attachments, then external links.
  const onSave = useCallback(
    async (payload: SavePayload, meta: SaveMeta) => {
      if (!meta.isEdit) {
        const createMsg = await create(payload);
        if (!createMsg?.success) return createMsg;
        if (meta.email && meta.externalLinks.length > 0) {
          const r = await setExternalLinks(meta.email, meta.externalLinks);
          if (!r?.success) return r;
        }
        return createMsg;
      }
      const updateMsg = await update(meta.email, payload);
      if (!updateMsg?.success) return updateMsg;
      const rawEmail = (payload as { email?: unknown }).email;
      const emailKey =
        typeof rawEmail === 'string' && rawEmail.trim() ? rawEmail.trim() : meta.email;
      if (meta.attach.length > 0) {
        const r = await attach(emailKey, meta.attach);
        if (!r?.success) return r;
      }
      if (meta.detach.length > 0) {
        const r = await detach(emailKey, meta.detach);
        if (!r?.success) return r;
      }
      const r = await setExternalLinks(emailKey, meta.externalLinks);
      if (!r?.success) return r;
      return updateMsg;
    },
    [create, update, attach, detach, setExternalLinks],
  );

  const onOpenChange = useCallback(
    (next: boolean) => {
      if (!next) onClose();
    },
    [onClose],
  );

  const isForm = request.kind === 'add' || request.kind === 'edit';
  const formOpen = open && isForm;
  const bulkOpen = open && request.kind === 'bulkAdd';
  const qrOpen = open && request.kind === 'qr';
  const infoOpen = open && request.kind === 'info';
  const viewed = request.kind === 'qr' || request.kind === 'info' ? request : null;

  return (
    <>
      <LazyMount when={formOpen}>
        <ClientFormModal
          open={formOpen}
          mode={request.kind === 'edit' ? 'edit' : 'add'}
          client={request.kind === 'edit' ? request.client : null}
          attachedIds={
            request.kind === 'edit'
              ? request.attachedIds
              : request.kind === 'add'
                ? [request.inboundId]
                : []
          }
          attachedExternalLinks={request.kind === 'edit' ? request.externalLinks : []}
          tunnelAllowedIPs={request.kind === 'edit' ? request.tunnelAllowedIPs : NO_TUNNEL_IPS}
          inbounds={inbounds}
          tgBotEnable={tgBotEnable}
          groups={groups}
          save={onSave}
          resetTraffic={resetTraffic}
          onOpenChange={onOpenChange}
        />
      </LazyMount>
      <LazyMount when={bulkOpen}>
        <ClientBulkAddModal
          open={bulkOpen}
          inbounds={inbounds}
          groups={groups}
          presetInboundIds={request.kind === 'bulkAdd' ? [request.inboundId] : undefined}
          onOpenChange={onOpenChange}
        />
      </LazyMount>
      <LazyMount when={qrOpen}>
        <ClientQrModal
          open={qrOpen}
          client={viewed?.client ?? null}
          inboundsById={inboundsById}
          tunnelAllowedIPs={viewed?.tunnelAllowedIPs ?? NO_TUNNEL_IPS}
          subSettings={subSettings}
          hosts={hosts}
          onOpenChange={onOpenChange}
        />
      </LazyMount>
      <LazyMount when={infoOpen}>
        <ClientInfoModal
          open={infoOpen}
          client={viewed?.client ?? null}
          inboundsById={inboundsById}
          tunnelAllowedIPs={viewed?.tunnelAllowedIPs ?? NO_TUNNEL_IPS}
          isOnline={viewed?.online ?? false}
          subSettings={subSettings}
          hosts={hosts}
          onOpenChange={onOpenChange}
        />
      </LazyMount>
    </>
  );
}

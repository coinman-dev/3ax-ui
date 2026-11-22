import type { ClientQueryParams, ClientRecord, ExternalLink } from '@/hooks/useClients';
import { SizeFormatter } from '@/utils';

// 3AX-UI: pure pieces of the 1.x inbounds-and-clients page (pages/inbounds/legacy).
export type AxClientBucket = 'active' | 'deactive' | 'depleted' | 'expiring';

export type AxClientModalRequest =
  | { kind: 'add' | 'bulkAdd'; inboundId: number }
  | {
      kind: 'edit';
      client: ClientRecord;
      attachedIds: number[];
      externalLinks: ExternalLink[];
      tunnelAllowedIPs: Record<number, string>;
    }
  | {
      kind: 'qr' | 'info';
      client: ClientRecord;
      tunnelAllowedIPs: Record<number, string>;
      online: boolean;
    };

export type AxClientRow = Pick<ClientRecord, 'enable' | 'totalGB' | 'expiryTime' | 'traffic'>;

const ONE_DAY_MS = 86_400_000;
export const AX_NESTED_MAX_PAGE_SIZE = 200;

function usedBytes(row: AxClientRow): number {
  return (row.traffic?.up || 0) + (row.traffic?.down || 0);
}

// Same rules as the Clients page, so a client reads the same on both pages.
export function axClientBucket(
  row: AxClientRow,
  now: number,
  expireDiff: number,
  trafficDiff: number,
): AxClientBucket {
  const used = usedBytes(row);
  const total = row.totalGB || 0;
  const expiry = row.expiryTime ?? 0;
  if ((expiry > 0 && expiry <= now) || (total > 0 && used >= total)) return 'depleted';
  if (!row.enable) return 'deactive';
  const nearExpiry = expiry > 0 && expiry - now < (expireDiff || 0);
  const nearLimit = total > 0 && total - used < (trafficDiff || 0);
  return nearExpiry || nearLimit ? 'expiring' : 'active';
}

export function axRemaining(row: AxClientRow): { label: string; color: string } {
  const total = row.totalGB || 0;
  if (total <= 0) return { label: '∞', color: 'purple' };
  const used = usedBytes(row);
  const left = total - used;
  const ratio = used / total;
  return {
    label: left > 0 ? SizeFormatter.sizeFormat(left) : '0',
    color: ratio >= 1 ? 'red' : ratio >= 0.85 ? 'orange' : 'green',
  };
}

export function axExpiryColor(expiryTime: number | undefined, now: number): string {
  if (!expiryTime) return 'purple';
  if (expiryTime < 0) return 'blue';
  if (expiryTime <= now) return 'red';
  return expiryTime - now < 3 * ONE_DAY_MS ? 'orange' : 'green';
}

export function axDelayedDays(expiryTime: number): number {
  return Math.round(expiryTime / -ONE_DAY_MS);
}

// The server caps a page at 200 rows; a panel page size of 0 means one long page.
export function axNestedQuery(
  inboundId: number,
  page: number,
  panelPageSize: number,
): ClientQueryParams {
  const pageSize =
    panelPageSize > 0 ? Math.min(panelPageSize, AX_NESTED_MAX_PAGE_SIZE) : AX_NESTED_MAX_PAGE_SIZE;
  return { page, pageSize, inbound: String(inboundId) };
}

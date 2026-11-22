// 3AX-UI: sortable column headers on the clients page.
export type SortOrder = 'ascend' | 'descend';

export interface ClientSort {
  column: string;
  order: SortOrder;
}

// The keys the paging API orders by (applyOrder in internal/web/service/client_paging.go).
const SERVER_SORT_KEYS = new Set([
  'createdAt',
  'updatedAt',
  'lastOnline',
  'enable',
  'email',
  'inboundIds',
  'traffic',
  'remaining',
  'expiryTime',
]);

// Speed exists only in the browser, sampled from live traffic, so it orders the loaded page.
export const LOCAL_SORT_KEY = 'speed';

export function parseClientSort(
  value: string | null | undefined,
  fallback: ClientSort,
): ClientSort {
  const [column, order] = (value ?? '').split(':');
  const sortable = SERVER_SORT_KEYS.has(column) || column === LOCAL_SORT_KEY;
  if (!sortable || (order !== 'ascend' && order !== 'descend')) return fallback;
  return { column, order };
}

export function serverSortKey(column: string | null): string | undefined {
  if (!column || column === LOCAL_SORT_KEY) return undefined;
  return column;
}

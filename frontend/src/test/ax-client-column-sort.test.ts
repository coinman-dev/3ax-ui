import { describe, expect, it } from 'vitest';

import { parseClientSort, serverSortKey } from '@/pages/clients/axColumnSort';

const fallback = { column: 'createdAt', order: 'ascend' as const };

// 3AX-UI: a header click can pick a direction the sort menu does not list
// (traffic ascending); the page must come back to it, not to the default.
describe('client column sorting', () => {
  it('restores any sortable column and direction', () => {
    expect(parseClientSort('traffic:ascend', fallback)).toEqual({
      column: 'traffic',
      order: 'ascend',
    });
    expect(parseClientSort('speed:descend', fallback)).toEqual({
      column: 'speed',
      order: 'descend',
    });
    expect(parseClientSort('createdAt:descend', fallback)).toEqual({
      column: 'createdAt',
      order: 'descend',
    });
  });

  it('falls back on anything it cannot sort by', () => {
    expect(parseClientSort('password:ascend', fallback)).toEqual(fallback);
    expect(parseClientSort('email:sideways', fallback)).toEqual(fallback);
    expect(parseClientSort(undefined, fallback)).toEqual(fallback);
  });

  it('keeps speed out of the server query, which cannot order by it', () => {
    expect(serverSortKey('speed')).toBeUndefined();
    expect(serverSortKey('inboundIds')).toBe('inboundIds');
    expect(serverSortKey(null)).toBeUndefined();
  });
});

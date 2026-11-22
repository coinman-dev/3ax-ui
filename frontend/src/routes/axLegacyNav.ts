import { useMemo } from 'react';

import { useLegacyInboundsView } from '@/hooks/useLegacyInboundsView';
import { PANEL_ROUTES, PANEL_TITLE_KEYS, type PanelRoute } from './panelRoutes';

export const AX_INBOUNDS_AND_CLIENTS_TITLE_KEY = 'menu.inboundsAndClients';

// 3AX-UI: the 1.x layout lists clients under their inbounds, so the menu drops the Clients
// entry; /clients itself stays routed and reachable by URL.
export function axNavRoutes(routes: PanelRoute[], legacyInboundsView: boolean): PanelRoute[] {
  if (!legacyInboundsView) return routes;
  return routes
    .filter((route) => route.path !== '/clients')
    .map((route) =>
      route.path === '/inbounds'
        ? { ...route, titleKey: AX_INBOUNDS_AND_CLIENTS_TITLE_KEY }
        : route,
    );
}

export function axTitleKey(path: string, legacyInboundsView: boolean): string | undefined {
  if (legacyInboundsView && path === '/inbounds') return AX_INBOUNDS_AND_CLIENTS_TITLE_KEY;
  return PANEL_TITLE_KEYS[path];
}

export function useAxNavRoutes(): PanelRoute[] {
  const legacyInboundsView = useLegacyInboundsView();
  return useMemo(() => axNavRoutes(PANEL_ROUTES, legacyInboundsView), [legacyInboundsView]);
}

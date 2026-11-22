import { useEffect } from 'react';
import { useLocation } from 'react-router';
import { useTranslation } from 'react-i18next';

import { axTitleKey } from '@/routes/axLegacyNav';
import { useLegacyInboundsView } from '@/hooks/useLegacyInboundsView';

export function usePageTitle() {
  const { pathname } = useLocation();
  const { t } = useTranslation();
  const legacyInboundsView = useLegacyInboundsView();

  useEffect(() => {
    const key = axTitleKey(pathname, legacyInboundsView);
    const title = key ? t(key) : '3AX-UI';
    const host = window.location.hostname;
    document.title = host ? `${host} - ${title}` : title;
  }, [pathname, t, legacyInboundsView]);
}

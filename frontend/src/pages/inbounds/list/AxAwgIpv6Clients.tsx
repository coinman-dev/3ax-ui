import { useTranslation } from 'react-i18next';

import { coerceInboundJsonField } from '@/models/dbinbound';

// 3AX-UI: the AmneziaVPN desktop app drops tunnel IPv6; these clients keep it.
const IPV6_CLIENTS = [
  { label: 'Win', href: 'https://github.com/amnezia-vpn/amneziawg-windows-client' },
  { label: 'Linux', href: 'https://github.com/coinman-dev/awg-linux-client' },
];

function servesIpv6(settings: unknown): boolean {
  const server = coerceInboundJsonField(settings).server;
  return coerceInboundJsonField(server).ipv6Enabled === true;
}

export function AxAwgIpv6Clients({ settings }: { settings: unknown }) {
  const { t } = useTranslation();
  if (!servesIpv6(settings)) return null;
  return (
    <div className="ax-awg-ipv6-clients">
      {t('pages.inbounds.axAwgIpv6Clients')}{' '}
      {IPV6_CLIENTS.map((client, i) => (
        <span key={client.href}>
          {i > 0 && ', '}
          <a href={client.href} target="_blank" rel="noopener noreferrer">
            {client.label}
          </a>
        </span>
      ))}
    </div>
  );
}

import type { TFunction } from 'i18next';

import type { NginxChange, NginxSettings, NginxStatus, NginxWarning } from '@/schemas/api/nginx';

// The service sends a warning as a code plus positional params, since it runs with no
// locale; each code's params are named here in the order nginx_service.go emits them.
export const NGINX_WARNINGS: Record<string, { key: string; params: readonly string[] }> = {
  noStreamModule: { key: 'pages.nginx.warnNoStreamModule', params: [] },
  notInstalled: { key: 'pages.nginx.notInstalled', params: [] },
  noDomain: { key: 'pages.nginx.warnNoDomain', params: [] },
  stockCoverPage: { key: 'pages.nginx.warnStockCoverPage', params: ['name'] },
  certExpiring: { key: 'pages.nginx.warnCertExpiring', params: ['domain', 'date'] },
  sniConflict: { key: 'pages.nginx.warnSniConflict', params: ['first', 'second', 'sni'] },
  sniIsOwnDomain: { key: 'pages.nginx.warnSniIsOwnDomain', params: ['inbound', 'domain'] },
  nothingToRoute: { key: 'pages.nginx.warnNothingToRoute', params: [] },
  noFirewall: { key: 'pages.nginx.warnNoFirewall', params: [] },
  certMissingFor: { key: 'pages.nginx.warnCertMissingFor', params: ['domain'] },
  certProblem: { key: 'pages.nginx.warnCertProblem', params: ['domain'] },
  stubExternal: { key: 'pages.nginx.warnStubExternal', params: ['hosts'] },
  firewallExtraUnreadable: { key: 'pages.nginx.warnFirewallExtraUnreadable', params: ['ports'] },
  inboundsUnreadable: { key: 'pages.nginx.warnInboundsUnreadable', params: [] },
  portConflict: { key: 'pages.nginx.warnPortConflict', params: ['port', 'inbound'] },
};

export function nginxWarningText(warning: NginxWarning, t: TFunction): string {
  const wording = NGINX_WARNINGS[warning.code];
  // A code with no wording of its own (configInvalid) carries the error from below as is.
  if (!wording) return warning.text || warning.code;
  const values = Object.fromEntries(
    wording.params.map((name, i) => [name, warning.params?.[i] ?? '']),
  );
  const sentence = t(wording.key, values);
  return warning.text ? `${sentence} (${warning.text})` : sentence;
}

export const NGINX_CHANGE_KEYS: Record<string, string> = {
  move: 'pages.nginx.planMove',
  links: 'pages.nginx.planLinks',
  serve: 'pages.nginx.planServe',
  dual: 'pages.nginx.planDual',
  subs: 'pages.nginx.planSubs',
  kept: 'pages.nginx.planKept',
  closed: 'pages.nginx.planClosed',
};

export function nginxChangeText(change: NginxChange, t: TFunction): string {
  const key = NGINX_CHANGE_KEYS[change.kind];
  if (!key) return `${change.subject}: ${change.from} → ${change.to}`;
  return t(key, { subject: change.subject, from: change.from, to: change.to });
}

export interface NginxPortRoute {
  label: string;
  dual: boolean;
  publicPort: number;
  port: number;
}

// "443 ↔ 4343" for an inbound that kept its own port, "443 ← 8443" for one that gave
// the public port up and is reachable only through nginx.
export function nginxPortRoute(
  status: Pick<NginxStatus, 'mode' | 'publicPort' | 'routes'>,
  inboundId: number,
): NginxPortRoute | null {
  if (status.mode === 'off') return null;
  const route = status.routes.find((r) => r.inboundId === inboundId);
  if (!route) return null;
  const arrow = route.dual ? '↔' : '←';
  return {
    label: `${status.publicPort} ${arrow} ${route.port}`,
    dual: route.dual,
    publicPort: status.publicPort,
    port: route.port,
  };
}

export interface PanelLocation {
  protocol: string;
  hostname: string;
  port: string;
  href: string;
}

export interface PanelAddressChange {
  disrupts: boolean;
  leaving443: boolean;
  url: string;
}

// Where the panel answers once these settings are applied, and whether the current
// connection may stop working on the way there.
export function panelAddressAfterApply(
  settings: Pick<NginxSettings, 'mode' | 'panelBehind443' | 'manageFirewall' | 'domain'>,
  panelPort: number,
  basePath: string,
  here: PanelLocation,
): PanelAddressChange {
  const behind443 = settings.mode === 'only443' && settings.panelBehind443;
  const on443 = here.port === '443' || (here.port === '' && here.protocol === 'https:');
  const entering443 = !on443 && behind443;
  const leaving443 = on443 && !behind443;
  const closesPorts = settings.mode === 'only443' && settings.manageFirewall;
  const page = `${basePath}panel/nginx`;
  let url = here.href;
  if (leaving443) {
    url = `${here.protocol}//${here.hostname}:${panelPort || here.port}${page}`;
  } else if (behind443) {
    url = `https://${settings.domain.trim() || here.hostname}${page}`;
  }
  return { disrupts: entering443 || leaving443 || closesPorts, leaving443, url };
}

export function formatCountdown(seconds: number): string {
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}

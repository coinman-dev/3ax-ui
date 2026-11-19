import { readFileSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';

import i18next from 'i18next';
import { beforeAll, describe, expect, it } from 'vitest';

import { INTERPOLATION } from '@/i18n/react';
import {
  NGINX_CHANGE_KEYS,
  NGINX_WARNINGS,
  nginxChangeText,
  nginxPortRoute,
  nginxWarningText,
  panelAddressAfterApply,
} from '@/lib/nginx/format';
import type { NginxRoute } from '@/schemas/api/nginx';

// 3AX-UI nginx front-end: the wording of the service's warning codes and plan lines, the
// inbound port label, and where the panel lives after an apply.

const translationDir = resolve(process.cwd(), '..', 'internal', 'web', 'translation');
type Bundle = Record<string, unknown>;
const bundles: Record<string, Bundle> = Object.fromEntries(
  readdirSync(translationDir)
    .filter((f) => f.endsWith('.json'))
    .map((f) => [
      f.replace('.json', ''),
      JSON.parse(readFileSync(join(translationDir, f), 'utf8')),
    ]),
);

function lookup(bundle: Bundle, key: string): string {
  const value = key.split('.').reduce<unknown>((node, part) => (node as Bundle)?.[part], bundle);
  if (typeof value !== 'string') throw new Error(`${key} is not a string`);
  return value;
}

function leafKeys(node: Bundle, prefix: string): string[] {
  return Object.entries(node).flatMap(([k, v]) =>
    typeof v === 'string' ? [`${prefix}.${k}`] : leafKeys(v as Bundle, `${prefix}.${k}`),
  );
}

function placeholders(text: string): string[] {
  return [...text.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
}

const i18n = i18next.createInstance();
const t = i18n.t.bind(i18n);
beforeAll(async () => {
  await i18n.init({
    lng: 'en-US',
    resources: { 'en-US': { translation: bundles['en-US'] } },
    interpolation: INTERPOLATION,
  });
});

describe('nginx translations', () => {
  const nginxKeys = [
    ...leafKeys((bundles['en-US'].pages as Bundle).nginx as Bundle, 'pages.nginx'),
    'pages.inbounds.portBoth',
    'pages.inbounds.portViaOnly',
  ];

  it('name the params each warning code is sent with', () => {
    for (const [code, { key, params }] of Object.entries(NGINX_WARNINGS)) {
      expect(placeholders(lookup(bundles['en-US'], key)), code).toEqual([...params].sort());
    }
  });

  it('build plan lines from subject/from/to only', () => {
    for (const key of Object.values(NGINX_CHANGE_KEYS)) {
      const used = placeholders(lookup(bundles['en-US'], key));
      expect(used.length, key).toBeGreaterThan(0);
      expect(
        used.filter((p) => !['subject', 'from', 'to'].includes(p)),
        key,
      ).toEqual([]);
    }
  });

  it('keep the en-US placeholders and no printf leftovers in every locale', () => {
    for (const [locale, bundle] of Object.entries(bundles)) {
      for (const key of nginxKeys) {
        const text = lookup(bundle, key);
        expect(placeholders(text), `${locale} ${key}`).toEqual(
          placeholders(lookup(bundles['en-US'], key)),
        );
        expect(text, `${locale} ${key}`).not.toMatch(/%s|%subject%|%from%|%to%/);
      }
    }
  });
});

describe('nginxWarningText', () => {
  it('fills a code’s positional params into its sentence', () => {
    const text = nginxWarningText(
      { code: 'sniConflict', params: ['reality-a', 'mtproto-b', 'cdn.example.com'] },
      t,
    );
    expect(text).toBe(
      '«reality-a» and «mtproto-b» both use the cover domain cdn.example.com — nginx can only send it to one of them.',
    );
  });

  it('appends the error from below after the sentence', () => {
    const text = nginxWarningText(
      { code: 'certProblem', params: ['panel.example.com'], text: 'x509: expired' },
      t,
    );
    expect(text).toBe('The certificate for panel.example.com cannot be used. (x509: expired)');
  });

  it('shows a code with no wording of its own as the text it carries', () => {
    expect(
      nginxWarningText({ code: 'configInvalid', text: 'nginx: [emerg] bad directive' }, t),
    ).toBe('nginx: [emerg] bad directive');
  });
});

describe('nginxChangeText', () => {
  it('words a known change kind', () => {
    expect(nginxChangeText({ kind: 'links', subject: 'reality', from: '8443', to: '443' }, t)).toBe(
      'Links for «reality»: port 8443 → 443',
    );
  });

  it('falls back to the raw fields for a kind it does not know', () => {
    expect(nginxChangeText({ kind: 'warning', subject: 'x', from: 'a', to: 'b' }, t)).toBe(
      'x: a → b',
    );
  });
});

describe('nginxPortRoute', () => {
  const route = (inboundId: number, port: number, dual: boolean): NginxRoute => ({
    inboundId,
    remark: `#${inboundId}`,
    protocol: 'vless',
    snis: ['cdn.example.com'],
    port,
    listen: dual ? '' : '127.0.0.1',
    fallback: false,
    dual,
  });
  const status = {
    mode: 'shared' as const,
    publicPort: 443,
    routes: [route(1, 4343, true), route(2, 8443, false)],
  };

  it('shows both ways in for an inbound that kept its port', () => {
    expect(nginxPortRoute(status, 1)?.label).toBe('443 ↔ 4343');
  });

  it('shows the one way in for an inbound that gave the public port up', () => {
    expect(nginxPortRoute(status, 2)?.label).toBe('443 ← 8443');
  });

  it('says nothing for an inbound nginx does not route', () => {
    expect(nginxPortRoute(status, 3)).toBeNull();
  });

  it('says nothing while the front-end is off', () => {
    expect(nginxPortRoute({ ...status, mode: 'off' }, 1)).toBeNull();
  });
});

describe('panelAddressAfterApply', () => {
  const direct = {
    protocol: 'https:',
    hostname: 'panel.example.com',
    port: '2053',
    href: 'https://panel.example.com:2053/base/panel/nginx',
  };
  const via443 = { ...direct, port: '', href: 'https://panel.example.com/base/panel/nginx' };
  const settings = {
    mode: 'only443' as const,
    panelBehind443: true,
    manageFirewall: false,
    domain: 'site.example.com',
  };

  it('sends the operator to the domain on 443 when the panel moves behind it', () => {
    expect(panelAddressAfterApply(settings, 2053, '/base/', direct)).toEqual({
      disrupts: true,
      leaving443: false,
      url: 'https://site.example.com/base/panel/nginx',
    });
  });

  it('sends the operator back to the panel port when the panel leaves 443', () => {
    expect(panelAddressAfterApply({ ...settings, mode: 'shared' }, 2053, '/base/', via443)).toEqual(
      {
        disrupts: true,
        leaving443: true,
        url: 'https://panel.example.com:2053/base/panel/nginx',
      },
    );
  });

  it('keeps the address but warns when only the firewall closes ports', () => {
    expect(
      panelAddressAfterApply(
        { ...settings, panelBehind443: false, manageFirewall: true },
        2053,
        '/base/',
        direct,
      ),
    ).toEqual({ disrupts: true, leaving443: false, url: direct.href });
  });

  it('does not treat plain http on port 80 as being behind 443', () => {
    const http80 = { ...direct, protocol: 'http:', port: '', href: 'http://panel.example.com/' };
    expect(
      panelAddressAfterApply({ ...settings, mode: 'shared' }, 2053, '/', http80).disrupts,
    ).toBe(false);
  });
});

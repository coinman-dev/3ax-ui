import { useEffect, useState } from 'react';
import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { DefaultsPayloadSchema } from '@/schemas/defaults';

// 3AX-UI: the 1.x single inbounds-and-clients page, read once and shared by the sidebar and pages.
let cachedValue = false;
let fetched = false;
let pending: Promise<void> | null = null;
const listeners = new Set<(value: boolean) => void>();

async function loadOnce(): Promise<void> {
  if (fetched) return;
  if (pending) {
    await pending;
    return;
  }
  pending = (async () => {
    try {
      const msg = await HttpUtil.post('/panel/api/setting/defaultSettings', undefined, {
        silent: true,
      });
      if (msg?.success) {
        const validated = parseMsg(msg, DefaultsPayloadSchema, 'setting/defaultSettings');
        setLegacyInboundsView(validated.obj?.legacyInboundsView === true);
      }
    } finally {
      fetched = true;
      pending = null;
    }
  })();
  await pending;
}

export function setLegacyInboundsView(value: boolean) {
  fetched = true;
  cachedValue = value;
  listeners.forEach((fn) => fn(cachedValue));
}

export function useLegacyInboundsView() {
  const [legacyInboundsView, setLocal] = useState(cachedValue);
  useEffect(() => {
    listeners.add(setLocal);
    void loadOnce();
    return () => {
      listeners.delete(setLocal);
    };
  }, []);
  return legacyInboundsView;
}

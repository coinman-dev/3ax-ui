import { useEffect, useState } from 'react';
import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { DefaultsPayloadSchema } from '@/schemas/defaults';

// 3AX-UI: the panel-wide QR size, read once and shared by every QrPanel.
export const DEFAULT_QR_CODE_SIZE = 450;

let cachedValue = DEFAULT_QR_CODE_SIZE;
let fetched = false;
let pending: Promise<void> | null = null;
const listeners = new Set<(value: number) => void>();

async function loadOnce(): Promise<void> {
  if (fetched) return;
  if (pending) {
    await pending;
    return;
  }
  pending = (async () => {
    try {
      const msg = await HttpUtil.post('/panel/api/setting/defaultSettings');
      if (msg?.success) {
        const validated = parseMsg(msg, DefaultsPayloadSchema, 'setting/defaultSettings');
        setQrCodeSize(validated.obj?.qrCodeSize ?? DEFAULT_QR_CODE_SIZE);
      }
    } finally {
      fetched = true;
      pending = null;
    }
  })();
  await pending;
}

export function setQrCodeSize(value: number) {
  fetched = true;
  cachedValue = value || DEFAULT_QR_CODE_SIZE;
  listeners.forEach((fn) => fn(cachedValue));
}

export function useQrCodeSize() {
  const [qrCodeSize, setLocal] = useState(cachedValue);
  useEffect(() => {
    listeners.add(setLocal);
    void loadOnce();
    return () => {
      listeners.delete(setLocal);
    };
  }, []);
  return qrCodeSize;
}

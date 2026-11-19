import { z } from 'zod';

import { TrojanClientSchema } from './trojan';

// HTTP proxy inbound — a classic forward proxy. Accounts are user/pass pairs;
// `allowTransparent` exposes Xray's option to forward requests with the
// original Host header. 3AX-UI adds billable panel clients next to them.
export const HttpAccountSchema = z.object({
  user: z.string().min(1),
  pass: z.string().min(1),
});
export type HttpAccount = z.infer<typeof HttpAccountSchema>;

export const HttpInboundSettingsSchema = z.object({
  accounts: z.array(HttpAccountSchema).default([]),
  allowTransparent: z.boolean().default(false),
  clients: z.array(TrojanClientSchema).optional(),
});
export type HttpInboundSettings = z.infer<typeof HttpInboundSettingsSchema>;

import { z } from 'zod';

// 3AX-UI nginx front-end: mirrors NginxSettings/NginxStatus/NginxPlan in
// internal/web/service/nginx_service.go and StubCard/StubTemplate in stub_service.go.

const nullableList = <T extends z.ZodType>(item: T) =>
  z
    .array(item)
    .nullish()
    .transform((list) => list ?? []);

export const NginxModeSchema = z.preprocess(
  (value) => (value === '' ? 'off' : value),
  z.enum(['off', 'shared', 'only443']),
);
export type NginxMode = z.infer<typeof NginxModeSchema>;

export const NginxSettingsSchema = z.object({
  mode: NginxModeSchema,
  domain: z.string(),
  stubSiteId: z.number().int(),
  subsBehind443: z.boolean(),
  panelBehind443: z.boolean(),
  manageFirewall: z.boolean(),
  firewallExtra: z.string(),
  realityPort: z
    .number('pages.nginx.realityPortInvalid')
    .int('pages.nginx.realityPortInvalid')
    .min(1, 'pages.nginx.realityPortInvalid')
    .max(65535, 'pages.nginx.realityPortInvalid')
    .refine((port) => port !== 443, 'pages.nginx.realityPortInvalid'),
  httpPort: z.number().int(),
});
export type NginxSettings = z.infer<typeof NginxSettingsSchema>;

export const NginxWarningSchema = z.object({
  code: z.string(),
  params: z.array(z.string()).nullish(),
  text: z.string().nullish(),
});
export type NginxWarning = z.infer<typeof NginxWarningSchema>;

export const NginxRouteSchema = z.object({
  inboundId: z.number(),
  remark: z.string(),
  protocol: z.string(),
  snis: nullableList(z.string()),
  port: z.number(),
  listen: z.string(),
  fallback: z.boolean(),
  dual: z.boolean(),
});
export type NginxRoute = z.infer<typeof NginxRouteSchema>;

export const NginxStatusSchema = z.object({
  installed: z.boolean(),
  version: z.string(),
  hasStream: z.boolean(),
  running: z.boolean(),
  mode: NginxModeSchema,
  domain: z.string(),
  certFile: z.string(),
  certOk: z.boolean(),
  certExpiry: z.number(),
  publicPort: z.number(),
  panelPort: z.number(),
  firewallOn: z.boolean(),
  confirmDeadline: z.number(),
  routes: nullableList(NginxRouteSchema),
  warnings: nullableList(NginxWarningSchema),
});
export type NginxStatus = z.infer<typeof NginxStatusSchema>;

export const NginxChangeSchema = z.object({
  kind: z.string(),
  subject: z.string(),
  from: z.string(),
  to: z.string(),
});
export type NginxChange = z.infer<typeof NginxChangeSchema>;

export const NginxPlanSchema = z.object({
  mode: z.string(),
  changes: nullableList(NginxChangeSchema),
  blockers: nullableList(NginxWarningSchema),
});
export type NginxPlan = z.infer<typeof NginxPlanSchema>;

export const StubCardSchema = z.object({
  key: z.string(),
  id: z.number(),
  name: z.string(),
  size: z.number(),
  active: z.boolean(),
  html: z.string().optional(),
});
export type StubCard = z.infer<typeof StubCardSchema>;

export const StubTemplateSchema = z.object({
  key: z.string(),
  name: z.string(),
  html: z.string(),
  size: z.number(),
});
export type StubTemplate = z.infer<typeof StubTemplateSchema>;

export const StubSiteSchema = z.object({
  id: z.number(),
  name: z.string(),
  html: z.string(),
  active: z.boolean(),
  size: z.number(),
  createdAt: z.number(),
  updatedAt: z.number(),
});

export const StubSaveResultSchema = z.object({
  site: StubSiteSchema,
  warnings: nullableList(NginxWarningSchema),
});

export const StubFormSchema = z.object({
  id: z.number().int(),
  name: z.string().trim().min(1, 'pages.nginx.stubNameRequired'),
  html: z.string().refine((html) => html.trim() !== '', 'pages.nginx.stubHtmlRequired'),
});
export type StubFormValues = z.infer<typeof StubFormSchema>;

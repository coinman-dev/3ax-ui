import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import i18next from 'i18next';
import type { z } from 'zod';

import { HttpError, httpRequest } from '@/api/http-init';
import { HttpUtil } from '@/utils';
import { getMessage } from '@/utils/messageBus';
import {
  NginxPlanSchema,
  NginxSettingsSchema,
  NginxStatusSchema,
  StubCardSchema,
  StubSaveResultSchema,
  StubSiteSchema,
  StubTemplateSchema,
  type NginxSettings,
  type StubCard,
  type StubFormValues,
} from '@/schemas/api/nginx';

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

// 3AX-UI keys live here rather than in queryKeys.ts so upstream merges never touch them.
export const nginxKeys = {
  root: () => ['nginx'] as const,
  status: () => ['nginx', 'status'] as const,
  settings: () => ['nginx', 'settings'] as const,
  certificate: (domain: string) => ['nginx', 'certificate', domain] as const,
  stubs: () => ['nginx', 'stubs'] as const,
  stubTemplates: () => ['nginx', 'stubTemplates'] as const,
};

async function fetchObj<S extends z.ZodType>(
  url: string,
  schema: S,
  params?: Record<string, string>,
): Promise<z.infer<S>> {
  const msg = await HttpUtil.get(url, params, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || `GET ${url} failed`);
  return schema.parse(msg.obj);
}

// Polling while a confirmation is pending notices both a rollback by the 30s job and a
// confirmation sent from another tab.
export function useNginxStatus({ pollWhilePending = false } = {}) {
  return useQuery({
    queryKey: nginxKeys.status(),
    queryFn: () => fetchObj('/panel/api/nginx/status', NginxStatusSchema),
    staleTime: 15_000,
    refetchInterval: (query) =>
      pollWhilePending && query.state.data?.confirmDeadline ? 10_000 : false,
  });
}

// Never refetched behind the operator's back: the page edits a copy of these.
export function useNginxSettings() {
  return useQuery({
    queryKey: nginxKeys.settings(),
    queryFn: () => fetchObj('/panel/api/nginx/settings', NginxSettingsSchema),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });
}

export function useNginxCertificate(domain: string) {
  return useQuery({
    queryKey: nginxKeys.certificate(domain),
    queryFn: () => fetchObj('/panel/api/nginx/certificate', NginxStatusSchema, { domain }),
    enabled: domain !== '',
    staleTime: 30_000,
  });
}

export function useNginxStubs() {
  return useQuery({
    queryKey: nginxKeys.stubs(),
    queryFn: () => fetchObj('/panel/api/nginx/stubs', StubCardSchema.array()),
  });
}

export function useNginxStubTemplates(enabled: boolean) {
  return useQuery({
    queryKey: nginxKeys.stubTemplates(),
    queryFn: () => fetchObj('/panel/api/nginx/stub-templates', StubTemplateSchema.array()),
    enabled,
    staleTime: Infinity,
  });
}

export async function fetchStubSite(id: number) {
  const msg = await HttpUtil.get(`/panel/api/nginx/stub/${id}`);
  return msg?.success ? StubSiteSchema.parse(msg.obj) : null;
}

export type ApplyOutcome = 'applied' | 'refused' | 'unanswered';

// HttpUtil folds a dropped connection into an ordinary failure, but here it is the
// expected outcome of moving the panel, so the transport error is kept apart.
async function postApply(settings: NginxSettings): Promise<ApplyOutcome> {
  try {
    const resp = await httpRequest('POST', '/panel/api/nginx/apply', settings, JSON_HEADERS);
    const data = resp.data as { success?: boolean; msg?: string } | null;
    if (data?.success) {
      if (data.msg) getMessage().success(data.msg);
      return 'applied';
    }
    getMessage().error(data?.msg || i18next.t('somethingWentWrong'));
    return 'refused';
  } catch (error) {
    if (error instanceof HttpError) {
      getMessage().error(error.message);
      return 'refused';
    }
    return 'unanswered';
  }
}

export function useNginxMutations() {
  const queryClient = useQueryClient();
  const invalidateAll = () => queryClient.invalidateQueries({ queryKey: nginxKeys.root() });
  const invalidateStubs = () => queryClient.invalidateQueries({ queryKey: nginxKeys.stubs() });

  const planMut = useMutation({
    mutationFn: async (settings: NginxSettings) => {
      const msg = await HttpUtil.post('/panel/api/nginx/plan', settings, JSON_HEADERS);
      return msg?.success ? NginxPlanSchema.parse(msg.obj) : null;
    },
  });

  const applyMut = useMutation({ mutationFn: postApply });

  const confirmMut = useMutation({
    mutationFn: () => HttpUtil.post('/panel/api/nginx/confirm'),
    onSuccess: (msg) => {
      if (msg?.success) return queryClient.invalidateQueries({ queryKey: nginxKeys.status() });
    },
  });

  const saveStubMut = useMutation({
    mutationFn: async (values: StubFormValues) => {
      const msg = await HttpUtil.post('/panel/api/nginx/stub/save', values, JSON_HEADERS);
      return msg?.success ? StubSaveResultSchema.parse(msg.obj) : null;
    },
    onSuccess: (result) => {
      if (result) return invalidateAll();
    },
  });

  const activateStubMut = useMutation({
    mutationFn: (card: StubCard) =>
      HttpUtil.post(
        card.id
          ? `/panel/api/nginx/stub/activate/${card.id}`
          : `/panel/api/nginx/stub/activate-template/${encodeURIComponent(card.key)}`,
      ),
    onSuccess: (msg) => {
      if (msg?.success) return invalidateAll();
    },
  });

  const deleteStubMut = useMutation({
    mutationFn: (id: number) => HttpUtil.post(`/panel/api/nginx/stub/del/${id}`),
    onSuccess: (msg) => {
      if (msg?.success) return invalidateStubs();
    },
  });

  return {
    plan: planMut.mutateAsync,
    planning: planMut.isPending,
    apply: applyMut.mutateAsync,
    applying: applyMut.isPending,
    confirm: confirmMut.mutateAsync,
    confirming: confirmMut.isPending,
    saveStub: saveStubMut.mutateAsync,
    savingStub: saveStubMut.isPending,
    activateStub: activateStubMut.mutateAsync,
    deleteStub: deleteStubMut.mutateAsync,
    invalidateAll,
  };
}

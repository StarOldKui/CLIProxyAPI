import { apiClient } from './client';

export interface CodexQuotaSnapshot {
  status: 'success' | 'error';
  status_code?: number;
  body?: string;
  error?: string;
  updated_at?: string;
  plan_type?: string;
  subscription_active_start?: string | number | null;
  subscription_active_until?: string | number | null;
  subscriptionActiveStart?: string | number | null;
  subscriptionActiveUntil?: string | number | null;
}

export interface CodexQuotaRefreshResponse {
  results: Record<string, CodexQuotaSnapshot>;
}

export const quotaApi = {
  refreshCodex: (names: string[]) =>
    apiClient.post<CodexQuotaRefreshResponse>('/codex-quota/refresh', { names })
};

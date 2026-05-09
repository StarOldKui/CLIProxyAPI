import { apiClient } from './client';

export interface QuotaSnapshot {
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
  profile_status_code?: number;
  profile_body?: string;
  supplementary_status_code?: number;
  supplementary_body?: string;
}

export interface QuotaRefreshResponse {
  results: Record<string, QuotaSnapshot>;
}

export const quotaApi = {
  refresh: (names: string[]) => apiClient.post<QuotaRefreshResponse>('/quota/refresh', { names }),
  refreshCodex: (names: string[]) =>
    apiClient.post<QuotaRefreshResponse>('/quota/refresh', { names }),
};

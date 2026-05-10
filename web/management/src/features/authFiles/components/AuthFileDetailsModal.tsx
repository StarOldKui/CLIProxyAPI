import { useMemo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import {
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  GEMINI_CLI_CONFIG,
  KIMI_CONFIG,
} from '@/components/quota';
import { ProviderStatusBar } from '@/components/providers/ProviderStatusBar';
import { useQuotaStore } from '@/stores';
import type {
  AntigravityQuotaState,
  AuthFileItem,
  ClaudeQuotaState,
  CodexQuotaState,
  GeminiCliQuotaState,
  KimiQuotaState,
} from '@/types';
import { formatFileSize } from '@/utils/format';
import {
  normalizeNumberValue,
  normalizePlanType,
  resolveAuthProvider,
  resolveCodexChatgptAccountId,
  resolveCodexPlanType,
  resolveGeminiCliProjectId,
} from '@/utils/quota';
import {
  normalizeRecentRequestBuckets,
  normalizeUsageTotal,
  statusBarDataFromRecentRequests,
} from '@/utils/recentRequests';
import {
  formatModified,
  getAuthFileStatusMessage,
  getTypeLabel,
  isRuntimeOnlyAuthFile,
  parsePriorityValue,
} from '@/features/authFiles/constants';
import { QuotaProgressBar } from '@/features/authFiles/components/QuotaProgressBar';
import { CredentialRequestEventsTable } from '@/features/authFiles/components/CredentialRequestEventsTable';
import styles from '@/pages/AuthFilesPage.module.scss';

export type AuthFileDetailsModalProps = {
  file: AuthFileItem | null;
  onClose: () => void;
  onCopyText: (text: string) => void | Promise<void>;
};

type DetailQuotaState =
  | AntigravityQuotaState
  | ClaudeQuotaState
  | CodexQuotaState
  | GeminiCliQuotaState
  | KimiQuotaState
  | null
  | undefined;

type DetailRow = {
  label: string;
  value: ReactNode;
  hidden?: boolean;
};

type QuotaLine = {
  id: string;
  label: string;
  percent: number | null;
  value: string;
  reset?: string;
};

const PREMIUM_CODEX_PLAN_TYPES = new Set(['pro', 'pro_lite']);
const normalizeDate = (value: unknown): Date | null => {
  if (value === null || value === undefined || value === '') return null;
  if (typeof value === 'number') {
    const date = new Date(value < 1e12 ? value * 1000 : value);
    return Number.isNaN(date.getTime()) ? null : date;
  }
  const date = new Date(String(value));
  return Number.isNaN(date.getTime()) ? null : date;
};

const formatDateTime = (value: unknown): string => {
  const date = normalizeDate(value);
  return date ? date.toLocaleString() : '-';
};

const formatTimeRemaining = (value: unknown, t: TFunction): string | null => {
  const date = normalizeDate(value);
  if (!date) return null;
  const remainingMs = date.getTime() - Date.now();
  if (remainingMs <= 0) return t('auth_files.details_expired');
  const days = Math.floor(remainingMs / 86_400_000);
  if (days > 0) return t('auth_files.details_days_remaining', { count: days });
  const hours = Math.floor(remainingMs / 3_600_000);
  if (hours > 0) return t('auth_files.details_hours_remaining', { count: hours });
  const minutes = Math.max(1, Math.floor(remainingMs / 60_000));
  return t('auth_files.details_minutes_remaining', { count: minutes });
};

const formatPlanLabel = (plan: unknown, t: TFunction): string => {
  const normalized = normalizePlanType(plan);
  if (!normalized) return '-';
  if (normalized === 'pro') return t('codex_quota.plan_pro');
  if (normalized === 'plus') return t('codex_quota.plan_plus');
  if (normalized === 'team') return t('codex_quota.plan_team');
  if (normalized === 'free') return t('codex_quota.plan_free');
  if (PREMIUM_CODEX_PLAN_TYPES.has(normalized)) return t('codex_quota.plan_prolite');
  return String(plan ?? normalized);
};

const formatPercent = (percent: number | null): string =>
  percent === null ? '--' : `${Math.round(Math.max(0, Math.min(100, percent)))}%`;

const toRemainingPercent = (usedPercent: number | null): number | null =>
  usedPercent === null ? null : 100 - Math.max(0, Math.min(100, usedPercent));

const parseStatusObject = (message: string): Record<string, unknown> | null => {
  if (!message) return null;
  try {
    const parsed = JSON.parse(message) as unknown;
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
};

const getNestedString = (value: unknown, path: string[]): string | null => {
  let current = value;
  for (const key of path) {
    if (!current || typeof current !== 'object' || Array.isArray(current)) return null;
    current = (current as Record<string, unknown>)[key];
  }
  return typeof current === 'string' && current.trim() ? current.trim() : null;
};

const getStatusDisplayMessage = (message: string): string => {
  const parsed = parseStatusObject(message);
  if (!parsed) return message;
  return (
    getNestedString(parsed, ['error', 'message']) ||
    getNestedString(parsed, ['error', 'type']) ||
    getNestedString(parsed, ['detail']) ||
    message
  );
};

const getStatusErrorType = (message: string): string | null => {
  const parsed = parseStatusObject(message);
  return (
    getNestedString(parsed, ['error', 'type']) ||
    getNestedString(parsed, ['error', 'code']) ||
    getNestedString(parsed, ['code'])
  );
};

const getQuotaError = (quota: DetailQuotaState): string => {
  if (!quota || quota.status !== 'error') return '';
  return quota.error || '';
};

const getQuotaConfig = (provider: string) => {
  if (provider === 'antigravity') return ANTIGRAVITY_CONFIG;
  if (provider === 'claude') return CLAUDE_CONFIG;
  if (provider === 'codex') return CODEX_CONFIG;
  if (provider === 'gemini-cli') return GEMINI_CLI_CONFIG;
  if (provider === 'kimi') return KIMI_CONFIG;
  return null;
};

const buildDiagnosis = (
  file: AuthFileItem,
  statusMessage: string,
  quota: DetailQuotaState,
  t: TFunction
) => {
  const displayMessage = getStatusDisplayMessage(statusMessage);
  const errorType = getStatusErrorType(statusMessage);
  const quotaError = getQuotaError(quota);
  const messageForMatch = `${errorType ?? ''} ${displayMessage} ${quotaError}`.toLowerCase();

  if (file.disabled) {
    return {
      tone: 'warning',
      title: t('auth_files.details_diagnosis_disabled_title'),
      body: t('auth_files.details_diagnosis_disabled_body'),
    };
  }
  if (messageForMatch.includes('usage_limit_reached')) {
    return {
      tone: 'warning',
      title: t('auth_files.details_diagnosis_quota_title'),
      body: t('auth_files.details_diagnosis_quota_body'),
    };
  }
  if (
    messageForMatch.includes('refresh_token_reused') ||
    messageForMatch.includes('invalidated') ||
    messageForMatch.includes('expired')
  ) {
    return {
      tone: 'danger',
      title: t('auth_files.details_diagnosis_auth_title'),
      body: t('auth_files.details_diagnosis_auth_body'),
    };
  }
  if (messageForMatch.includes('rate limit')) {
    return {
      tone: 'warning',
      title: t('auth_files.details_diagnosis_rate_title'),
      body: t('auth_files.details_diagnosis_rate_body'),
    };
  }
  if (displayMessage || quotaError) {
    return {
      tone: 'danger',
      title: t('auth_files.details_diagnosis_error_title'),
      body: displayMessage || quotaError,
    };
  }
  return {
    tone: 'success',
    title: t('auth_files.details_diagnosis_ready_title'),
    body: t('auth_files.details_diagnosis_ready_body'),
  };
};

const getQuotaLines = (provider: string, quota: DetailQuotaState, t: TFunction): QuotaLine[] => {
  if (!quota || quota.status !== 'success') return [];

  if (provider === 'codex') {
    return ((quota as CodexQuotaState).windows ?? []).map((window) => {
      const percent = toRemainingPercent(window.usedPercent);
      return {
        id: window.id,
        label: window.labelKey
          ? t(window.labelKey, window.labelParams as Record<string, string | number>)
          : window.label,
        percent,
        value: formatPercent(percent),
        reset: window.resetLabel,
      };
    });
  }

  if (provider === 'claude') {
    return ((quota as ClaudeQuotaState).windows ?? []).map((window) => {
      const percent = toRemainingPercent(window.usedPercent);
      return {
        id: window.id,
        label: window.labelKey ? t(window.labelKey) : window.label,
        percent,
        value: formatPercent(percent),
        reset: window.resetLabel,
      };
    });
  }

  if (provider === 'antigravity') {
    return ((quota as AntigravityQuotaState).groups ?? []).map((group) => {
      const percent = group.remainingFraction * 100;
      return {
        id: group.id,
        label: group.label,
        percent,
        value: formatPercent(percent),
        reset: group.resetTime ? formatDateTime(group.resetTime) : undefined,
      };
    });
  }

  if (provider === 'gemini-cli') {
    return ((quota as GeminiCliQuotaState).buckets ?? []).map((bucket) => {
      const percent =
        bucket.remainingFraction === null ? null : Math.max(0, bucket.remainingFraction * 100);
      const amount =
        bucket.remainingAmount === null || bucket.remainingAmount === undefined
          ? ''
          : ` · ${bucket.remainingAmount}`;
      return {
        id: bucket.id,
        label: bucket.label,
        percent,
        value: `${formatPercent(percent)}${amount}`,
        reset: bucket.resetTime ? formatDateTime(bucket.resetTime) : undefined,
      };
    });
  }

  if (provider === 'kimi') {
    return ((quota as KimiQuotaState).rows ?? []).map((row) => {
      const remaining = row.limit > 0 ? ((row.limit - row.used) / row.limit) * 100 : null;
      return {
        id: row.id,
        label: row.labelKey ? t(row.labelKey, row.labelParams) : row.label || row.id,
        percent: remaining,
        value: `${formatPercent(remaining)} · ${Math.max(0, row.limit - row.used)}/${row.limit}`,
        reset: row.resetHint,
      };
    });
  }

  return [];
};

const DetailField = ({ label, value, hidden }: DetailRow) => {
  if (hidden) return null;
  return (
    <div className={styles.detailField}>
      <span className={styles.detailFieldLabel}>{label}</span>
      <span className={styles.detailFieldValue}>{value || '-'}</span>
    </div>
  );
};

export function AuthFileDetailsModal(props: AuthFileDetailsModalProps) {
  const { t } = useTranslation();
  const { file, onClose, onCopyText } = props;
  const detailText = useMemo(() => (file ? JSON.stringify(file, null, 2) : ''), [file]);

  const liveQuota = useQuotaStore((state) => {
    if (!file) return null;
    const provider = resolveAuthProvider(file);
    if (provider === 'antigravity') return state.antigravityQuota[file.name];
    if (provider === 'claude') return state.claudeQuota[file.name];
    if (provider === 'codex') return state.codexQuota[file.name];
    if (provider === 'gemini-cli') return state.geminiCliQuota[file.name];
    if (provider === 'kimi') return state.kimiQuota[file.name];
    return null;
  }) as DetailQuotaState;

  const cachedQuota = useMemo(() => {
    if (!file) return null;
    const config = getQuotaConfig(resolveAuthProvider(file));
    return (config?.getCachedState?.(file, t) as DetailQuotaState) ?? null;
  }, [file, t]);

  if (!file) {
    return <Modal open={false} onClose={onClose} title={t('auth_files.details_title')} />;
  }

  const provider = resolveAuthProvider(file);
  const quota = cachedQuota ?? liveQuota;
  const quotaLines = getQuotaLines(provider, quota, t);
  const recentBuckets = normalizeRecentRequestBuckets(file.recent_requests ?? file.recentRequests);
  const statusData = statusBarDataFromRecentRequests(recentBuckets);
  const successCount = normalizeUsageTotal(file.success);
  const failureCount = normalizeUsageTotal(file.failed);
  const requestTotal = successCount + failureCount;
  const successRate =
    requestTotal > 0 ? `${((successCount / requestTotal) * 100).toFixed(1)}%` : '-';
  const statusMessage = getAuthFileStatusMessage(file);
  const displayStatusMessage = getStatusDisplayMessage(statusMessage);
  const diagnosis = buildDiagnosis(file, statusMessage, quota, t);
  const typeLabel = getTypeLabel(t, file.type || provider || 'unknown');
  const isRuntimeOnly = isRuntimeOnlyAuthFile(file);
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const priorityValue = parsePriorityValue(file.priority ?? file['priority']);
  const noteValue = typeof file.note === 'string' ? file.note.trim() : '';
  const codexPlan =
    provider === 'codex'
      ? ((quota as CodexQuotaState | null)?.planType ?? resolveCodexPlanType(file))
      : null;
  const subscriptionUntil =
    provider === 'codex' ? (quota as CodexQuotaState | null)?.subscriptionActiveUntil : null;
  const subscriptionRemaining = formatTimeRemaining(subscriptionUntil, t);
  const codexAccountId = provider === 'codex' ? resolveCodexChatgptAccountId(file) : null;
  const geminiProjectId = provider === 'gemini-cli' ? resolveGeminiCliProjectId(file) : null;
  const quotaStatus = quota?.status ?? 'idle';
  const quotaError = getQuotaError(quota);
  const diagnosisClass =
    diagnosis.tone === 'success'
      ? styles.detailDiagnosisSuccess
      : diagnosis.tone === 'warning'
        ? styles.detailDiagnosisWarning
        : styles.detailDiagnosisDanger;

  const overviewRows: DetailRow[] = [
    { label: t('auth_files.details_provider'), value: typeLabel },
    {
      label: t('auth_files.details_state'),
      value: file.disabled
        ? t('auth_files.health_status_disabled')
        : t('auth_files.details_enabled'),
    },
    {
      label: t('auth_files.details_runtime'),
      value: isRuntimeOnly ? t('common.yes') : t('common.no'),
    },
    {
      label: t('auth_files.details_credential_id'),
      value: rawAuthIndex == null ? '-' : String(rawAuthIndex),
    },
    {
      label: t('auth_files.file_size'),
      value: formatFileSize(normalizeNumberValue(file.size) ?? 0),
    },
    { label: t('auth_files.file_modified'), value: formatModified(file) },
    {
      label: t('auth_files.priority_display'),
      value: priorityValue ?? '-',
      hidden: priorityValue === undefined,
    },
    { label: t('auth_files.note_display'), value: noteValue, hidden: !noteValue },
  ];

  const identityRows: DetailRow[] = [
    {
      label: t('auth_files.details_account'),
      value: typeof file.account === 'string' ? file.account : '-',
    },
    {
      label: t('auth_files.details_codex_account_id'),
      value: codexAccountId ?? '-',
      hidden: provider !== 'codex',
    },
    {
      label: t('auth_files.details_gemini_project_id'),
      value: geminiProjectId ?? '-',
      hidden: provider !== 'gemini-cli',
    },
    { label: t('auth_files.details_last_refresh'), value: formatDateTime(file.lastRefresh) },
  ];

  return (
    <Modal
      open={Boolean(file)}
      onClose={onClose}
      width={920}
      title={
        <div className={styles.detailTitle}>
          <span>{t('auth_files.details_title')}</span>
          <small>{file.name}</small>
        </div>
      }
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t('common.close')}
          </Button>
          <Button
            variant="secondary"
            onClick={() => {
              if (!detailText) return;
              void onCopyText(detailText);
            }}
            disabled={!detailText}
          >
            {t('common.copy')}
          </Button>
        </>
      }
    >
      <div className={styles.detailContent}>
        <section className={styles.detailHero}>
          <div className={styles.detailHeroMain}>
            <span className={styles.detailTypeBadge}>{typeLabel}</span>
            <h3>{file.name}</h3>
            <p>{displayStatusMessage || t('auth_files.health_status_no_message')}</p>
          </div>
          <div className={styles.detailMetricGrid}>
            <div className={styles.detailMetric}>
              <span>{t('stats.success')}</span>
              <strong>{successCount}</strong>
            </div>
            <div className={styles.detailMetric}>
              <span>{t('stats.failure')}</span>
              <strong>{failureCount}</strong>
            </div>
            <div className={styles.detailMetric}>
              <span>{t('auth_files.details_success_rate')}</span>
              <strong>{successRate}</strong>
            </div>
          </div>
        </section>

        <div className={styles.detailGrid}>
          <section className={styles.detailPanel}>
            <h4>{t('auth_files.details_overview')}</h4>
            <div className={styles.detailFieldGrid}>
              {overviewRows.map((row) => (
                <DetailField key={row.label} {...row} />
              ))}
            </div>
          </section>

          <section className={styles.detailPanel}>
            <h4>{t('auth_files.details_identity')}</h4>
            <div className={styles.detailFieldGrid}>
              {identityRows.map((row) => (
                <DetailField key={row.label} {...row} />
              ))}
            </div>
          </section>
        </div>

        <section className={styles.detailPanel}>
          <div className={styles.detailSectionHeader}>
            <h4>{t('auth_files.details_quota_snapshot')}</h4>
            <span>{t(`auth_files.details_quota_status_${quotaStatus}`)}</span>
          </div>
          {provider === 'codex' && (
            <div className={styles.detailQuotaMeta}>
              <DetailField
                label={t('codex_quota.plan_label')}
                value={formatPlanLabel(codexPlan, t)}
              />
              <DetailField
                label={t('codex_quota.subscription_label')}
                value={
                  subscriptionRemaining
                    ? `${subscriptionRemaining} · ${formatDateTime(subscriptionUntil)}`
                    : '-'
                }
              />
            </div>
          )}
          {quotaStatus === 'error' ? (
            <div className={styles.detailMessage}>{quotaError || t('common.unknown_error')}</div>
          ) : quotaLines.length > 0 ? (
            <div className={styles.detailQuotaList}>
              {quotaLines.map((line) => (
                <div key={line.id} className={styles.detailQuotaLine}>
                  <div className={styles.detailQuotaLineHeader}>
                    <span>{line.label}</span>
                    <span>
                      <strong>{line.value}</strong>
                      {line.reset ? ` · ${line.reset}` : ''}
                    </span>
                  </div>
                  <QuotaProgressBar
                    percent={line.percent}
                    highThreshold={70}
                    mediumThreshold={30}
                  />
                </div>
              ))}
            </div>
          ) : (
            <div className={styles.detailMessage}>{t('auth_files.details_no_quota_snapshot')}</div>
          )}
        </section>

        <section className={styles.detailPanel}>
          <h4>{t('auth_files.details_activity')}</h4>
          <div className={styles.detailStatusBar}>
            <ProviderStatusBar statusData={statusData} styles={styles} />
          </div>
        </section>

        <section className={styles.detailPanel}>
          <div className={styles.detailSectionHeader}>
            <h4>{t('auth_files.details_events_title')}</h4>
            <span>{t('auth_files.details_events_scope')}</span>
          </div>
          <CredentialRequestEventsTable file={file} />
        </section>

        <section className={`${styles.detailPanel} ${diagnosisClass}`}>
          <h4>{diagnosis.title}</h4>
          <p>{diagnosis.body}</p>
          {statusMessage && <pre className={styles.detailMessage}>{statusMessage}</pre>}
        </section>

        <details className={styles.detailRawBlock}>
          <summary>{t('auth_files.details_raw_json')}</summary>
          <pre className={styles.jsonContent}>{detailText}</pre>
        </details>
      </div>
    </Modal>
  );
}

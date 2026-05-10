import { useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { useUsageStatsStore } from '@/stores/useUsageStatsStore';
import type { AuthFileItem } from '@/types';
import { parseTimestampMs } from '@/utils/timestamp';
import {
  extractLatencyMs,
  extractTotalTokens,
  formatDurationMs,
  formatUsageThinkingLabel,
  normalizeAuthIndex,
  type UsageDetail,
  type UsageThinking,
} from '@/utils/usage';
import styles from '@/pages/AuthFilesPage.module.scss';

type CredentialRequestEventsTableProps = {
  file: AuthFileItem;
};

type CredentialEventRow = {
  id: string;
  timestamp: string;
  timestampMs: number;
  timestampLabel: string;
  model: string;
  failed: boolean;
  latencyMs: number | null;
  thinking: UsageThinking | null;
  thinkingLabel: string;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  totalTokens: number;
  errorMessage: string;
};

const MAX_RENDERED_EVENTS = 500;

const toNumber = (value: unknown): number => {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
};

const buildRow = (
  detail: UsageDetail,
  index: number,
  locale: string
): CredentialEventRow => {
  const timestamp = detail.timestamp;
  const timestampMs =
    typeof detail.__timestampMs === 'number' && detail.__timestampMs > 0
      ? detail.__timestampMs
      : parseTimestampMs(timestamp);
  const date = Number.isNaN(timestampMs) ? null : new Date(timestampMs);
  const inputTokens = Math.max(toNumber(detail.tokens?.input_tokens), 0);
  const outputTokens = Math.max(toNumber(detail.tokens?.output_tokens), 0);
  const reasoningTokens = Math.max(toNumber(detail.tokens?.reasoning_tokens), 0);
  const cachedTokens = Math.max(
    Math.max(toNumber(detail.tokens?.cached_tokens), 0),
    Math.max(toNumber(detail.tokens?.cache_tokens), 0)
  );
  const totalTokens = Math.max(toNumber(detail.tokens?.total_tokens), extractTotalTokens(detail));
  const model = String(detail.__modelName ?? '').trim() || '-';
  const latencyMs = extractLatencyMs(detail);
  const thinking = detail.thinking ?? null;
  const errorMessage = typeof detail.error_message === 'string' ? detail.error_message.trim() : '';

  return {
    id: `${timestamp}-${model}-${detail.auth_index ?? ''}-${index}`,
    timestamp,
    timestampMs: Number.isNaN(timestampMs) ? 0 : timestampMs,
    timestampLabel: date ? date.toLocaleString(locale) : timestamp || '-',
    model,
    failed: detail.failed === true,
    latencyMs,
    thinking,
    thinkingLabel: formatUsageThinkingLabel(thinking),
    inputTokens,
    outputTokens,
    reasoningTokens,
    cachedTokens,
    totalTokens,
    errorMessage,
  };
};

export function CredentialRequestEventsTable({ file }: CredentialRequestEventsTableProps) {
  const { t, i18n } = useTranslation();
  const usageDetails = useUsageStatsStore((state) => state.usageDetails);
  const loading = useUsageStatsStore((state) => state.loading);
  const error = useUsageStatsStore((state) => state.error);
  const loadUsageStats = useUsageStatsStore((state) => state.loadUsageStats);
  const authIndex = normalizeAuthIndex(file['auth_index'] ?? file.authIndex);

  useEffect(() => {
    void loadUsageStats().catch(() => {});
  }, [loadUsageStats]);

  const rows = useMemo(() => {
    if (!authIndex) return [];
    return usageDetails
      .filter((detail) => normalizeAuthIndex(detail.auth_index) === authIndex)
      .map((detail, index) => buildRow(detail, index, i18n.language))
      .sort((a, b) => b.timestampMs - a.timestampMs);
  }, [authIndex, i18n.language, usageDetails]);

  const renderedRows = rows.slice(0, MAX_RENDERED_EVENTS);
  const summary = rows.reduce(
    (acc, row) => {
      if (!row.failed) acc.successCount += 1;
      acc.totalTokens += row.totalTokens;
      if (row.latencyMs !== null) {
        acc.latencyTotal += row.latencyMs;
        acc.latencyCount += 1;
      }
      return acc;
    },
    { successCount: 0, totalTokens: 0, latencyTotal: 0, latencyCount: 0 }
  );
  const failureCount = rows.length - summary.successCount;
  const averageLatency =
    summary.latencyCount > 0 ? summary.latencyTotal / summary.latencyCount : null;

  if (!authIndex) {
    return (
      <div className={styles.detailMessage}>{t('auth_files.details_events_missing_id')}</div>
    );
  }

  if (loading && usageDetails.length === 0) {
    return <div className={styles.detailMessage}>{t('common.loading')}</div>;
  }

  if (error && usageDetails.length === 0) {
    return <div className={styles.detailMessage}>{error}</div>;
  }

  if (rows.length === 0) {
    return <div className={styles.detailMessage}>{t('auth_files.details_events_empty')}</div>;
  }

  return (
    <div className={styles.credentialEvents}>
      <div className={styles.credentialEventsSummary}>
        <span>{t('auth_files.details_events_count', { count: rows.length })}</span>
        <span>
          {t('stats.success')} {summary.successCount.toLocaleString()} · {t('stats.failure')}{' '}
          {failureCount.toLocaleString()}
        </span>
        <span>
          {t('usage_stats.total_tokens')} {summary.totalTokens.toLocaleString()}
        </span>
        <span>
          {t('auth_files.details_events_avg_latency')}{' '}
          {averageLatency === null ? '-' : formatDurationMs(averageLatency)}
        </span>
      </div>
      {rows.length > MAX_RENDERED_EVENTS && (
        <div className={styles.credentialEventsLimit}>
          {t('auth_files.details_events_limit', {
            shown: MAX_RENDERED_EVENTS,
            total: rows.length,
          })}
        </div>
      )}
      <div className={styles.credentialEventsTableWrapper}>
        <table className={styles.credentialEventsTable}>
          <thead>
            <tr>
              <th>{t('usage_stats.request_events_timestamp')}</th>
              <th>{t('usage_stats.model_name')}</th>
              <th>{t('usage_stats.request_events_result')}</th>
              <th>{t('usage_stats.time')}</th>
              <th>{t('usage_stats.thinking_intensity')}</th>
              <th>{t('usage_stats.input_tokens')}</th>
              <th>{t('usage_stats.output_tokens')}</th>
              <th>{t('usage_stats.reasoning_tokens')}</th>
              <th>{t('usage_stats.cached_tokens')}</th>
              <th>{t('usage_stats.total_tokens')}</th>
              <th>{t('usage_stats.error_message')}</th>
            </tr>
          </thead>
          <tbody>
            {renderedRows.map((row) => (
              <tr key={row.id}>
                <td title={row.timestamp}>{row.timestampLabel}</td>
                <td>{row.model}</td>
                <td>
                  <span
                    className={
                      row.failed
                        ? styles.credentialEventsResultFailed
                        : styles.credentialEventsResultSuccess
                    }
                  >
                    {row.failed ? t('stats.failure') : t('stats.success')}
                  </span>
                </td>
                <td>{formatDurationMs(row.latencyMs)}</td>
                <td>
                  <span
                    className={
                      row.thinking
                        ? styles.credentialEventsThinkingBadge
                        : styles.credentialEventsThinkingEmpty
                    }
                  >
                    {row.thinkingLabel}
                  </span>
                </td>
                <td>{row.inputTokens.toLocaleString()}</td>
                <td>{row.outputTokens.toLocaleString()}</td>
                <td>{row.reasoningTokens.toLocaleString()}</td>
                <td>{row.cachedTokens.toLocaleString()}</td>
                <td>{row.totalTokens.toLocaleString()}</td>
                <td className={styles.credentialEventsError} title={row.errorMessage}>
                  {row.errorMessage || '-'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

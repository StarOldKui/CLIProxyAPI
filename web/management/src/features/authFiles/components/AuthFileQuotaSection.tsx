import { type ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import {
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  GEMINI_CLI_CONFIG,
  KIMI_CONFIG,
} from '@/components/quota';
import { useQuotaStore } from '@/stores';
import type { AuthFileItem } from '@/types';
import { resolveQuotaErrorMessage, type QuotaProviderType } from '@/features/authFiles/constants';
import { QuotaProgressBar } from '@/features/authFiles/components/QuotaProgressBar';
import styles from '@/pages/AuthFilesPage.module.scss';

type QuotaState = { status?: string; error?: string; errorStatus?: number } | undefined;

const getQuotaConfig = (type: QuotaProviderType) => {
  if (type === 'antigravity') return ANTIGRAVITY_CONFIG;
  if (type === 'claude') return CLAUDE_CONFIG;
  if (type === 'codex') return CODEX_CONFIG;
  if (type === 'kimi') return KIMI_CONFIG;
  return GEMINI_CLI_CONFIG;
};

export type AuthFileQuotaSectionProps = {
  file: AuthFileItem;
  quotaType: QuotaProviderType;
  disableControls: boolean;
  onRefreshQuota: (file: AuthFileItem) => void;
};

export function AuthFileQuotaSection(props: AuthFileQuotaSectionProps) {
  const { disableControls, file, onRefreshQuota, quotaType } = props;
  const { t } = useTranslation();

  const quota = useQuotaStore((state) => {
    if (quotaType === 'antigravity') return state.antigravityQuota[file.name] as QuotaState;
    if (quotaType === 'claude') return state.claudeQuota[file.name] as QuotaState;
    if (quotaType === 'codex') return state.codexQuota[file.name] as QuotaState;
    if (quotaType === 'kimi') return state.kimiQuota[file.name] as QuotaState;
    return state.geminiCliQuota[file.name] as QuotaState;
  });

  const config = getQuotaConfig(quotaType) as unknown as {
    i18nPrefix: string;
    getCachedState?: (file: AuthFileItem, t: TFunction) => unknown;
    renderQuotaItems: (quota: unknown, t: TFunction, helpers: unknown) => unknown;
  };

  const displayedQuota = (config.getCachedState?.(file, t) as QuotaState) ?? quota;
  const quotaStatus = displayedQuota?.status ?? 'idle';
  const canRefresh = !disableControls && file.disabled !== true && quotaStatus !== 'loading';
  const quotaErrorMessage = resolveQuotaErrorMessage(
    t,
    displayedQuota?.errorStatus,
    displayedQuota?.error || t('common.unknown_error')
  );
  const renderIdleMessage = () => (
    <button
      type="button"
      className={`${styles.quotaMessage} ${styles.quotaMessageAction}`}
      onClick={() => onRefreshQuota(file)}
      disabled={!canRefresh}
    >
      {t(`${config.i18nPrefix}.idle`)}
    </button>
  );

  return (
    <div className={styles.quotaSection}>
      {quotaStatus === 'loading' ? (
        <div className={styles.quotaMessage}>{t(`${config.i18nPrefix}.loading`)}</div>
      ) : quotaStatus === 'idle' ? (
        renderIdleMessage()
      ) : quotaStatus === 'error' ? (
        <div className={styles.quotaError}>
          {t(`${config.i18nPrefix}.load_failed`, {
            message: quotaErrorMessage,
          })}
        </div>
      ) : displayedQuota ? (
        (config.renderQuotaItems(displayedQuota, t, { styles, QuotaProgressBar }) as ReactNode)
      ) : (
        renderIdleMessage()
      )}
    </div>
  );
}

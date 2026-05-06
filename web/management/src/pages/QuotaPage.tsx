/**
 * Quota management page - coordinates the three quota sections.
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useAuthStore } from '@/stores';
import { authFilesApi, configFileApi } from '@/services/api';
import {
  QuotaSection,
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  GEMINI_CLI_CONFIG,
  KIMI_CONFIG
} from '@/components/quota';
import type { AuthFileItem } from '@/types';
import styles from './QuotaPage.module.scss';

const QUOTA_FILE_REFRESH_INTERVAL_MS = 60_000;

export function QuotaPage() {
  const { t } = useTranslation();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);

  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const loadedRef = useRef(false);
  const requestIdRef = useRef(0);
  const latestFilesRef = useRef<AuthFileItem[]>([]);

  const disableControls = connectionStatus !== 'connected';

  const loadConfig = useCallback(async () => {
    try {
      await configFileApi.fetchConfigYaml();
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError((prev) => prev || errorMessage);
    }
  }, [t]);

  const loadFiles = useCallback(async (): Promise<AuthFileItem[]> => {
    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;
    const isInitialLoad = !loadedRef.current;
    setLoading(isInitialLoad);
    setError('');
    try {
      const data = await authFilesApi.list();
      const nextFiles = data?.files || [];
      if (requestId !== requestIdRef.current) return latestFilesRef.current;
      latestFilesRef.current = nextFiles;
      setFiles(nextFiles);
      loadedRef.current = true;
      return nextFiles;
    } catch (err: unknown) {
      if (requestId !== requestIdRef.current) return latestFilesRef.current;
      const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(errorMessage);
      return latestFilesRef.current;
    } finally {
      if (requestId === requestIdRef.current) {
        setLoading(false);
      }
    }
  }, [t]);

  const refreshPageData = useCallback(async (): Promise<AuthFileItem[]> => {
    const [nextFiles] = await Promise.all([loadFiles(), loadConfig()]);
    return nextFiles;
  }, [loadConfig, loadFiles]);

  const handleHeaderRefresh = useCallback(async () => {
    await refreshPageData();
  }, [refreshPageData]);

  useHeaderRefresh(handleHeaderRefresh);

  useEffect(() => {
    loadFiles();
    loadConfig();
  }, [loadFiles, loadConfig]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      void loadFiles();
    }, QUOTA_FILE_REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [loadFiles]);

  return (
    <div className={styles.container}>
      <div className={styles.pageHeader}>
        <h1 className={styles.pageTitle}>{t('quota_management.title')}</h1>
        <p className={styles.description}>{t('quota_management.description')}</p>
      </div>

      {error && <div className={styles.errorBox}>{error}</div>}

      <QuotaSection
        config={CLAUDE_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        onRefreshFiles={refreshPageData}
      />
      <QuotaSection
        config={ANTIGRAVITY_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        onRefreshFiles={refreshPageData}
      />
      <QuotaSection
        config={CODEX_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        onRefreshFiles={refreshPageData}
      />
      <QuotaSection
        config={GEMINI_CLI_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        onRefreshFiles={refreshPageData}
      />
      <QuotaSection
        config={KIMI_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        onRefreshFiles={refreshPageData}
      />
    </div>
  );
}

/**
 * Generic quota section component.
 */

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { useNotificationStore, useQuotaStore, useThemeStore } from '@/stores';
import type { AuthFileItem, ResolvedTheme } from '@/types';
import { getStatusFromError } from '@/utils/quota';
import { QuotaCard } from './QuotaCard';
import type { QuotaStatusState } from './QuotaCard';
import { useQuotaLoader } from './useQuotaLoader';
import type { QuotaConfig } from './quotaConfigs';
import { useGridColumns } from './useGridColumns';
import { IconRefreshCw } from '@/components/ui/icons';
import styles from '@/pages/QuotaPage.module.scss';

type QuotaUpdater<T> = T | ((prev: T) => T);

type QuotaSetter<T> = (updater: QuotaUpdater<T>) => void;

type ViewMode = 'paged' | 'all';

const MAX_ITEMS_PER_PAGE = 25;

interface QuotaPaginationState<T> {
  pageSize: number;
  totalPages: number;
  currentPage: number;
  pageItems: T[];
  setPageSize: (size: number) => void;
  goToPrev: () => void;
  goToNext: () => void;
  loading: boolean;
  loadingScope: 'page' | 'all' | null;
  setLoading: (loading: boolean, scope?: 'page' | 'all' | null) => void;
}

const useQuotaPagination = <T,>(items: T[], defaultPageSize = 6): QuotaPaginationState<T> => {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSizeState] = useState(defaultPageSize);
  const [loading, setLoadingState] = useState(false);
  const [loadingScope, setLoadingScope] = useState<'page' | 'all' | null>(null);

  const totalPages = useMemo(
    () => Math.max(1, Math.ceil(items.length / pageSize)),
    [items.length, pageSize]
  );

  const currentPage = useMemo(() => Math.min(page, totalPages), [page, totalPages]);

  const pageItems = useMemo(() => {
    const start = (currentPage - 1) * pageSize;
    return items.slice(start, start + pageSize);
  }, [items, currentPage, pageSize]);

  const setPageSize = useCallback((size: number) => {
    setPageSizeState(size);
    setPage(1);
  }, []);

  const goToPrev = useCallback(() => {
    setPage((prev) => Math.max(1, prev - 1));
  }, []);

  const goToNext = useCallback(() => {
    setPage((prev) => Math.min(totalPages, prev + 1));
  }, [totalPages]);

  const setLoading = useCallback((isLoading: boolean, scope?: 'page' | 'all' | null) => {
    setLoadingState(isLoading);
    setLoadingScope(isLoading ? (scope ?? null) : null);
  }, []);

  return {
    pageSize,
    totalPages,
    currentPage,
    pageItems,
    setPageSize,
    goToPrev,
    goToNext,
    loading,
    loadingScope,
    setLoading
  };
};

interface QuotaSectionProps<TState extends QuotaStatusState, TData> {
  config: QuotaConfig<TState, TData>;
  files: AuthFileItem[];
  loading: boolean;
  disabled: boolean;
  onRefreshFiles: () => Promise<AuthFileItem[]>;
}

export function QuotaSection<TState extends QuotaStatusState, TData>({
  config,
  files,
  loading,
  disabled,
  onRefreshFiles
}: QuotaSectionProps<TState, TData>) {
  const { t } = useTranslation();
  const resolvedTheme: ResolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const setQuota = useQuotaStore((state) => state[config.storeSetter]) as QuotaSetter<
    Record<string, TState>
  >;

  const [columns, gridRef] = useGridColumns(380);
  const [viewMode, setViewMode] = useState<ViewMode>('all');
  const [refreshingFiles, setRefreshingFiles] = useState(false);
  const { quota, loadQuota } = useQuotaLoader(config);

  const quotaFileNames = useMemo(
    () => files.filter((file) => config.filterFn(file)).map((file) => file.name),
    [files, config]
  );
  const quotaFileNameKey = useMemo(() => quotaFileNames.join('\n'), [quotaFileNames]);

  const filteredFiles = useMemo(() => {
    const matched = files.filter((file) => config.filterFn(file));
    return config.sortFiles ? config.sortFiles(matched, quota) : matched;
  }, [files, config, quota]);

  const cachedQuotaKey = useMemo(() => {
    if (!config.getCachedState) return '';
    return filteredFiles
      .map((file) => `${file.name}:${config.getCachedStateKey?.(file) ?? ''}`)
      .join('\n');
  }, [filteredFiles, config]);

  const {
    pageSize,
    totalPages,
    currentPage,
    pageItems,
    setPageSize,
    goToPrev,
    goToNext,
    loading: sectionLoading,
    setLoading
  } = useQuotaPagination(filteredFiles);

  // Update page size based on view mode and columns
  useEffect(() => {
    if (viewMode === 'all') {
      setPageSize(Math.max(1, filteredFiles.length));
    } else {
      // Paged mode: 3 rows * columns, capped to avoid oversized pages.
      setPageSize(Math.min(columns * 3, MAX_ITEMS_PER_PAGE));
    }
  }, [viewMode, columns, filteredFiles.length, setPageSize]);

  const handleRefresh = useCallback(async () => {
    if (sectionLoading || loading || refreshingFiles) return;
    const refreshScope = viewMode === 'paged' ? 'page' : 'all';
    const currentPageNames = new Set(pageItems.map((file) => file.name));
    setRefreshingFiles(true);
    try {
      const latestFiles = await onRefreshFiles();
      const matched = latestFiles.filter((file) => config.filterFn(file));
      const sortedTargets = config.sortFiles ? config.sortFiles(matched, quota) : matched;
      const targets =
        refreshScope === 'page'
          ? sortedTargets.filter((file) => currentPageNames.has(file.name))
          : sortedTargets;
      if (targets.length === 0) return;
      await loadQuota(targets, refreshScope, setLoading);
    } finally {
      setRefreshingFiles(false);
    }
  }, [
    config,
    loadQuota,
    loading,
    onRefreshFiles,
    pageItems,
    quota,
    refreshingFiles,
    sectionLoading,
    setLoading,
    viewMode
  ]);

  useEffect(() => {
    if (loading) return;
    if (!config.getCachedState) return;
    setQuota((prev) => {
      let changed = false;
      const nextState = { ...prev };

      filteredFiles.forEach((file) => {
        const cached = config.getCachedState?.(file, t);
        if (!cached) return;

        const current = prev[file.name] as (TState & { status?: string; updatedAt?: string | null }) | undefined;
        if (current?.status === 'loading') return;

        const cachedUpdatedAt = (cached as TState & { updatedAt?: string | null }).updatedAt;
        const currentUpdatedAt = current?.updatedAt;
        if (current) {
          if (!cachedUpdatedAt) return;
          if (currentUpdatedAt === cachedUpdatedAt) return;
          if (currentUpdatedAt) {
            const cachedTime = Date.parse(cachedUpdatedAt);
            const currentTime = Date.parse(currentUpdatedAt);
            if (!Number.isNaN(cachedTime) && !Number.isNaN(currentTime) && cachedTime <= currentTime) {
              return;
            }
          }
        }

        nextState[file.name] = cached;
        changed = true;
      });

      return changed ? nextState : prev;
    });
  }, [loading, cachedQuotaKey, filteredFiles, config, setQuota, t]);

  useEffect(() => {
    if (loading) return;
    const allowedNames = new Set(quotaFileNames);
    setQuota((prev) => {
      const previousNames = Object.keys(prev);
      if (quotaFileNames.length === 0) {
        return previousNames.length === 0 ? prev : {};
      }

      let changed = false;
      const nextState: Record<string, TState> = {};

      previousNames.forEach((name) => {
        if (!allowedNames.has(name)) {
          changed = true;
        }
      });

      quotaFileNames.forEach((name) => {
        const cached = prev[name];
        if (cached) {
          nextState[name] = cached;
        }
      });

      return changed ? nextState : prev;
    });
  }, [loading, quotaFileNameKey, quotaFileNames, setQuota]);

  const refreshQuotaForFile = useCallback(
    async (file: AuthFileItem) => {
      if (disabled || file.disabled) return;
      if (quota[file.name]?.status === 'loading') return;

      setQuota((prev) => ({
        ...prev,
        [file.name]: config.buildLoadingState()
      }));

      try {
        const data = await config.fetchQuota(file, t);
        setQuota((prev) => ({
          ...prev,
          [file.name]: config.buildSuccessState(data)
        }));
        showNotification(t('auth_files.quota_refresh_success', { name: file.name }), 'success');
      } catch (err: unknown) {
        const message = err instanceof Error ? err.message : t('common.unknown_error');
        const status = getStatusFromError(err);
        setQuota((prev) => ({
          ...prev,
          [file.name]: config.buildErrorState(message, status)
        }));
        showNotification(
          t('auth_files.quota_refresh_failed', { name: file.name, message }),
          'error'
        );
      }
    },
    [config, disabled, quota, setQuota, showNotification, t]
  );

  const titleNode = (
    <div className={styles.titleWrapper}>
      <span>{t(`${config.i18nPrefix}.title`)}</span>
      {filteredFiles.length > 0 && (
        <span className={styles.countBadge}>
          {filteredFiles.length}
        </span>
      )}
    </div>
  );

  const isRefreshing = sectionLoading || loading || refreshingFiles;
  const refreshButtonLabel = t(
    viewMode === 'paged'
      ? 'quota_management.refresh_current_page_credentials'
      : 'quota_management.refresh_all_credentials'
  );
  const groups = useMemo(
    () =>
      config.groupFiles
        ? config.groupFiles(pageItems, quota, t)
        : [{ key: 'all', label: '', files: pageItems }],
    [config, pageItems, quota, t]
  );

  return (
    <Card
      title={titleNode}
      extra={
        <div className={styles.headerActions}>
          <div className={styles.viewModeToggle}>
            <Button
              variant="secondary"
              size="sm"
              className={`${styles.viewModeButton} ${
                viewMode === 'paged' ? styles.viewModeButtonActive : ''
              }`}
              onClick={() => setViewMode('paged')}
            >
              {t('auth_files.view_mode_paged')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              className={`${styles.viewModeButton} ${
                viewMode === 'all' ? styles.viewModeButtonActive : ''
              }`}
              onClick={() => setViewMode('all')}
            >
              {t('auth_files.view_mode_all')}
            </Button>
          </div>
          <Button
            variant="secondary"
            size="sm"
            className={styles.refreshAllButton}
            onClick={handleRefresh}
            disabled={disabled || isRefreshing}
            loading={isRefreshing}
            title={refreshButtonLabel}
            aria-label={refreshButtonLabel}
          >
            {!isRefreshing && <IconRefreshCw size={16} />}
            {refreshButtonLabel}
          </Button>
        </div>
      }
    >
      {loading && filteredFiles.length === 0 ? (
        <div className={styles.quotaMessage}>{t('common.loading')}</div>
      ) : filteredFiles.length === 0 ? (
        <EmptyState
          title={t(`${config.i18nPrefix}.empty_title`)}
          description={t(`${config.i18nPrefix}.empty_desc`)}
        />
      ) : (
        <>
          <div ref={gridRef} className={styles.quotaGroups}>
            {groups.map((group) => (
              <div key={group.key} className={styles.quotaGroup}>
                {group.label && <h3 className={styles.quotaGroupTitle}>{group.label}</h3>}
                <div className={config.gridClassName}>
                  {group.files.map((item) => (
                    <QuotaCard
                      key={item.name}
                      item={item}
                      quota={quota[item.name]}
                      resolvedTheme={resolvedTheme}
                      i18nPrefix={config.i18nPrefix}
                      cardIdleMessageKey={config.cardIdleMessageKey}
                      cardClassName={config.cardClassName}
                      defaultType={config.type}
                      canRefresh={!disabled && !item.disabled}
                      onRefresh={() => void refreshQuotaForFile(item)}
                      renderQuotaItems={config.renderQuotaItems}
                    />
                  ))}
                </div>
              </div>
            ))}
          </div>
          {filteredFiles.length > pageSize && viewMode === 'paged' && (
            <div className={styles.pagination}>
              <Button
                variant="secondary"
                size="sm"
                onClick={goToPrev}
                disabled={currentPage <= 1}
              >
                {t('auth_files.pagination_prev')}
              </Button>
              <div className={styles.pageInfo}>
                {t('auth_files.pagination_info', {
                  current: currentPage,
                  total: totalPages,
                  count: filteredFiles.length
                })}
              </div>
              <Button
                variant="secondary"
                size="sm"
                onClick={goToNext}
                disabled={currentPage >= totalPages}
              >
                {t('auth_files.pagination_next')}
              </Button>
            </div>
          )}
        </>
      )}
    </Card>
  );
}

import {
  useCallback,
  type CSSProperties,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { animate } from 'motion/mini';
import type { AnimationPlaybackControlsWithThen } from 'motion-dom';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import { useInterval } from '@/hooks/useInterval';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { IconFilterAll } from '@/components/ui/icons';
import { EmptyState } from '@/components/ui/EmptyState';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { copyToClipboard } from '@/utils/clipboard';
import {
  MAX_CARD_PAGE_SIZE,
  MIN_CARD_PAGE_SIZE,
  QUOTA_PROVIDER_TYPES,
  clampCardPageSize,
  getAuthFileIcon,
  getTypeColor,
  getTypeLabel,
  hasAuthFileProblemStatus,
  isUsageLimitReachedAuthFile,
  isUsageLimitReachedMessage,
  isRuntimeOnlyAuthFile,
  normalizeProviderKey,
  parsePriorityValue,
  type QuotaProviderType,
  type ResolvedTheme,
} from '@/features/authFiles/constants';
import { AuthFileCard } from '@/features/authFiles/components/AuthFileCard';
import { AuthFileDetailsModal } from '@/features/authFiles/components/AuthFileDetailsModal';
import { AuthFileModelsModal } from '@/features/authFiles/components/AuthFileModelsModal';
import { AuthFilesPrefixProxyEditorModal } from '@/features/authFiles/components/AuthFilesPrefixProxyEditorModal';
import { OAuthExcludedCard } from '@/features/authFiles/components/OAuthExcludedCard';
import { OAuthModelAliasCard } from '@/features/authFiles/components/OAuthModelAliasCard';
import { useAuthFilesData } from '@/features/authFiles/hooks/useAuthFilesData';
import { useAuthFilesModels } from '@/features/authFiles/hooks/useAuthFilesModels';
import { useAuthFilesOauth } from '@/features/authFiles/hooks/useAuthFilesOauth';
import { useAuthFilesPrefixProxyEditor } from '@/features/authFiles/hooks/useAuthFilesPrefixProxyEditor';
import { useAuthFilesStatusBarCache } from '@/features/authFiles/hooks/useAuthFilesStatusBarCache';
import {
  isAuthFilesSortMode,
  readAuthFilesUiState,
  readPersistedAuthFilesCompactMode,
  writeAuthFilesUiState,
  writePersistedAuthFilesCompactMode,
  type AuthFilesSortMode,
} from '@/features/authFiles/uiState';
import {
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  GEMINI_CLI_CONFIG,
  KIMI_CONFIG,
  quotaSnapshotFromFile,
} from '@/components/quota';
import { useAuthStore, useNotificationStore, useQuotaStore, useThemeStore } from '@/stores';
import { normalizePlanType, resolveAuthProvider, resolveCodexPlanType } from '@/utils/quota';
import styles from './AuthFilesPage.module.scss';

const easePower3Out = (progress: number) => 1 - (1 - progress) ** 4;
const easePower2In = (progress: number) => progress ** 3;
const BATCH_BAR_BASE_TRANSFORM = 'translateX(-50%)';
const BATCH_BAR_HIDDEN_TRANSFORM = 'translateX(-50%) translateY(56px)';
const DEFAULT_REGULAR_PAGE_SIZE = 9;
const DEFAULT_COMPACT_PAGE_SIZE = 12;
const AUTH_FILES_STATUS_REFRESH_INTERVAL_MS = 5_000;
const QUOTA_REFRESH_INTERVAL_MS = 5 * 60 * 1000;

type AuthQuotaState = { status?: string; error?: string; errorStatus?: number } | undefined;

type AuthQuotaMaps = Record<QuotaProviderType, Record<string, AuthQuotaState>>;

type QuotaRefreshSummary = {
  failed: number;
  lastRefreshAt: number | null;
  nextRefreshAt: number | null;
  success: number;
};

type ProviderFileGroup = {
  key: string;
  label: string;
  files: AuthFileItem[];
};

const escapeWildcardSearchSegment = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

const buildWildcardSearch = (value: string): RegExp | null => {
  if (!value.includes('*')) return null;
  const pattern = value.split('*').map(escapeWildcardSearchSegment).join('.*');
  return new RegExp(pattern, 'i');
};

const getQuotaConfig = (provider: string) => {
  if (provider === 'antigravity') return ANTIGRAVITY_CONFIG;
  if (provider === 'claude') return CLAUDE_CONFIG;
  if (provider === 'codex') return CODEX_CONFIG;
  if (provider === 'gemini-cli') return GEMINI_CLI_CONFIG;
  if (provider === 'kimi') return KIMI_CONFIG;
  return null;
};

const getCachedQuotaStateForAuthFile = (
  file: AuthFileItem,
  t: ReturnType<typeof useTranslation>['t']
): AuthQuotaState => {
  const provider = resolveAuthProvider(file);
  if (!QUOTA_PROVIDER_TYPES.has(provider as QuotaProviderType)) return undefined;
  return (getQuotaConfig(provider)?.getCachedState?.(file, t) as AuthQuotaState) ?? undefined;
};

const getQuotaStateForAuthFile = (
  file: AuthFileItem,
  quotaMaps: AuthQuotaMaps,
  t: ReturnType<typeof useTranslation>['t']
): AuthQuotaState => {
  const provider = resolveAuthProvider(file);
  if (!QUOTA_PROVIDER_TYPES.has(provider as QuotaProviderType)) return undefined;
  return (
    getCachedQuotaStateForAuthFile(file, t) ?? quotaMaps[provider as QuotaProviderType][file.name]
  );
};

const hasAuthFileQuotaSnapshotProblem = (file: AuthFileItem): boolean => {
  const snapshot = quotaSnapshotFromFile(file);
  return Boolean(
    snapshot &&
    typeof snapshot === 'object' &&
    !Array.isArray(snapshot) &&
    (snapshot as { status?: unknown; error?: unknown }).status === 'error' &&
    !isUsageLimitReachedMessage(String((snapshot as { error?: unknown }).error ?? ''))
  );
};

const hasAuthFileQuotaProblem = (
  file: AuthFileItem,
  quotaMaps: AuthQuotaMaps,
  t: ReturnType<typeof useTranslation>['t']
): boolean => {
  const quota = getQuotaStateForAuthFile(file, quotaMaps, t);
  if (quota) {
    return quota.status === 'error' && !isUsageLimitReachedMessage(quota.error ?? '');
  }
  return hasAuthFileQuotaSnapshotProblem(file);
};

const parseQuotaUpdatedAt = (value?: string | null): number | null => {
  if (!value) return null;
  const time = Date.parse(value);
  return Number.isNaN(time) ? null : time;
};

const resolveCodexQuotaSortWindowId = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined
): 'five-hour' | 'weekly' => {
  const plan = normalizePlanType(quota?.planType) ?? resolveCodexPlanType(file);
  return plan === 'free' ? 'weekly' : 'five-hour';
};

const getCodexRemainingQuotaScore = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined
): number | null => {
  if (!quota || quota.status !== 'success') return null;
  const windowId = resolveCodexQuotaSortWindowId(file, quota);
  const windows = Array.isArray(quota.windows) ? quota.windows : [];
  const window = windows.find((item) => item.id === windowId);
  const usedPercent = window?.usedPercent;
  if (typeof usedPercent !== 'number' || !Number.isFinite(usedPercent)) return null;
  const clampedUsed = Math.max(0, Math.min(100, usedPercent));
  return Math.max(0, Math.min(100, 100 - clampedUsed));
};

const isCodexQuotaExhausted = (file: AuthFileItem, quota: CodexQuotaState | undefined): boolean =>
  getCodexRemainingQuotaScore(file, quota) === 0;

const formatClockTime = (timestamp: number | null): string => {
  if (timestamp === null) return '';
  return new Date(timestamp).toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });
};

const formatCountdown = (remainingMs: number): string => {
  const totalSeconds = Math.max(0, Math.ceil(remainingMs / 1000));
  const minutes = String(Math.floor(totalSeconds / 60)).padStart(2, '0');
  const seconds = String(totalSeconds % 60).padStart(2, '0');
  return `${minutes}:${seconds}`;
};

const isAuthFileQuotaUsageLimited = (
  file: AuthFileItem,
  quotaMaps: AuthQuotaMaps,
  t: ReturnType<typeof useTranslation>['t']
): boolean => {
  const quota = getQuotaStateForAuthFile(file, quotaMaps, t);
  if (quota) {
    if (quota.status === 'error' && isUsageLimitReachedMessage(quota.error ?? '')) return true;
    if (resolveAuthProvider(file) === 'codex') {
      return isCodexQuotaExhausted(file, quota as CodexQuotaState);
    }
    return false;
  }

  const snapshot = quotaSnapshotFromFile(file);
  if (!snapshot || typeof snapshot !== 'object' || Array.isArray(snapshot)) return false;

  if (
    (snapshot as { status?: unknown; error?: unknown }).status === 'error' &&
    isUsageLimitReachedMessage(String((snapshot as { error?: unknown }).error ?? ''))
  ) {
    return true;
  }

  if (resolveAuthProvider(file) === 'codex') {
    return isCodexQuotaExhausted(file, snapshot as CodexQuotaState);
  }
  return false;
};

const DisplayOptionIcon = ({ type }: { type: 'problem' | 'disabled' | 'quota' | 'compact' }) => {
  if (type === 'problem') {
    return (
      <svg
        aria-hidden="true"
        className={styles.filterToggleIcon}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <path d="M12 9v4" />
        <path d="M10.363 3.591l-8.106 13.534a1.914 1.914 0 0 0 1.636 2.871h16.214a1.914 1.914 0 0 0 1.636 -2.87l-8.106 -13.536a1.914 1.914 0 0 0 -3.274 0" />
        <path d="M12 16h.01" />
      </svg>
    );
  }

  if (type === 'disabled') {
    return (
      <svg
        aria-hidden="true"
        className={styles.filterToggleIcon}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <path d="M20.042 16.045a9 9 0 0 0 -12.087 -12.087m-2.318 1.677a9 9 0 1 0 12.725 12.73" />
        <path d="M3 3l18 18" />
      </svg>
    );
  }

  if (type === 'quota') {
    return (
      <svg
        aria-hidden="true"
        className={styles.filterToggleIcon}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <path d="M4 13a8 8 0 0 1 7 7a6 6 0 0 0 3 -5a9 9 0 0 0 6 -8a3 3 0 0 0 -3 -3a9 9 0 0 0 -8 6a6 6 0 0 0 -5 3" />
        <path d="M7 14a6 6 0 0 0 -3 6a6 6 0 0 0 6 -3" />
        <path d="M14 9a1 1 0 1 0 2 0a1 1 0 1 0 -2 0" />
      </svg>
    );
  }

  return (
    <svg
      aria-hidden="true"
      className={styles.filterToggleIcon}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M12 21a9 9 0 1 1 0 -18a9 9 0 0 1 0 18" />
      <path d="M10 10c-.5 -1 -2.5 -1 -3 0" />
      <path d="M17 10c-.5 -1 -2.5 -1 -3 0" />
      <path d="M14.5 15a3.5 3.5 0 0 1 -5 0" />
    </svg>
  );
};

export function AuthFilesPage() {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const resolvedTheme: ResolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const antigravityQuota = useQuotaStore(
    (state) => state.antigravityQuota as Record<string, AuthQuotaState>
  );
  const claudeQuota = useQuotaStore((state) => state.claudeQuota as Record<string, AuthQuotaState>);
  const codexQuota = useQuotaStore((state) => state.codexQuota as Record<string, AuthQuotaState>);
  const geminiCliQuota = useQuotaStore(
    (state) => state.geminiCliQuota as Record<string, AuthQuotaState>
  );
  const kimiQuota = useQuotaStore((state) => state.kimiQuota as Record<string, AuthQuotaState>);
  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.status === 'current' : true;
  const navigate = useNavigate();

  const [filter, setFilter] = useState<'all' | string>('all');
  const [problemOnly, setProblemOnly] = useState(false);
  const [disabledOnly, setDisabledOnly] = useState(false);
  const [hideUsageLimited, setHideUsageLimited] = useState(false);
  const [compactMode, setCompactMode] = useState(false);
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(1);
  const [pageSizeByMode, setPageSizeByMode] = useState({
    regular: DEFAULT_REGULAR_PAGE_SIZE,
    compact: DEFAULT_COMPACT_PAGE_SIZE,
  });
  const [pageSizeInput, setPageSizeInput] = useState('9');
  const [viewMode, setViewMode] = useState<'diagram' | 'list'>('list');
  const [sortMode, setSortMode] = useState<AuthFilesSortMode>('quota');
  const [detailsFile, setDetailsFile] = useState<AuthFileItem | null>(null);
  const [batchActionBarVisible, setBatchActionBarVisible] = useState(false);
  const [uiStateHydrated, setUiStateHydrated] = useState(false);
  const [quotaRefreshNowMs, setQuotaRefreshNowMs] = useState(() => Date.now());
  const floatingBatchActionsRef = useRef<HTMLDivElement>(null);
  const batchActionAnimationRef = useRef<AnimationPlaybackControlsWithThen | null>(null);
  const previousSelectionCountRef = useRef(0);
  const selectionCountRef = useRef(0);

  const {
    files,
    selectedFiles,
    selectionCount,
    loading,
    refreshing,
    error,
    uploading,
    deleting,
    deletingAll,
    statusUpdating,
    batchStatusUpdating,
    fileInputRef,
    loadFiles,
    handleUploadClick,
    handleFileChange,
    handleDelete,
    handleDeleteAll,
    handleDownload,
    handleStatusToggle,
    toggleSelect,
    selectAllVisible,
    invertVisibleSelection,
    deselectAll,
    batchDownload,
    batchSetStatus,
    batchDelete,
  } = useAuthFilesData();

  const statusBarCache = useAuthFilesStatusBarCache(files);

  const {
    excluded,
    excludedError,
    modelAlias,
    modelAliasError,
    allProviderModels,
    loadExcluded,
    loadModelAlias,
    deleteExcluded,
    deleteModelAlias,
    handleMappingUpdate,
    handleDeleteLink,
    handleToggleFork,
    handleRenameAlias,
    handleDeleteAlias,
  } = useAuthFilesOauth({ viewMode, files });

  const {
    modelsModalOpen,
    modelsLoading,
    modelsList,
    modelsFileName,
    modelsFileType,
    modelsError,
    showModels,
    closeModelsModal,
  } = useAuthFilesModels();

  const {
    prefixProxyEditor,
    prefixProxyUpdatedText,
    prefixProxyDirty,
    openPrefixProxyEditor,
    closePrefixProxyEditor,
    handlePrefixProxyChange,
    handlePrefixProxySave,
  } = useAuthFilesPrefixProxyEditor({
    disableControls: connectionStatus !== 'connected',
    loadFiles,
  });

  const disableControls = connectionStatus !== 'connected';
  const filesBusy = loading || refreshing;
  const normalizedFilter = normalizeProviderKey(String(filter));
  const quotaFilterType: QuotaProviderType | null = QUOTA_PROVIDER_TYPES.has(
    normalizedFilter as QuotaProviderType
  )
    ? (normalizedFilter as QuotaProviderType)
    : null;
  const quotaMaps = useMemo<AuthQuotaMaps>(
    () => ({
      antigravity: antigravityQuota,
      claude: claudeQuota,
      codex: codexQuota,
      'gemini-cli': geminiCliQuota,
      kimi: kimiQuota,
    }),
    [antigravityQuota, claudeQuota, codexQuota, geminiCliQuota, kimiQuota]
  );
  const pageSize = compactMode ? pageSizeByMode.compact : pageSizeByMode.regular;
  const isAllGroupedView = filter === 'all';
  const isCodexGroupedView = quotaFilterType === 'codex';
  const isGroupedView = isAllGroupedView || isCodexGroupedView;

  useEffect(() => {
    const persistedCompactMode = readPersistedAuthFilesCompactMode();
    if (typeof persistedCompactMode === 'boolean') {
      setCompactMode(persistedCompactMode);
    }

    const persisted = readAuthFilesUiState();
    if (persisted) {
      if (typeof persisted.filter === 'string' && persisted.filter.trim()) {
        setFilter(persisted.filter);
      }
      if (typeof persisted.problemOnly === 'boolean') {
        setProblemOnly(persisted.problemOnly);
      }
      if (typeof persisted.disabledOnly === 'boolean') {
        setDisabledOnly(persisted.disabledOnly);
      }
      if (typeof persisted.hideUsageLimited === 'boolean') {
        setHideUsageLimited(persisted.hideUsageLimited);
      }
      if (typeof persistedCompactMode !== 'boolean' && typeof persisted.compactMode === 'boolean') {
        setCompactMode(persisted.compactMode);
      }
      if (typeof persisted.search === 'string') {
        setSearch(persisted.search);
      }
      if (typeof persisted.page === 'number' && Number.isFinite(persisted.page)) {
        setPage(Math.max(1, Math.round(persisted.page)));
      }
      const legacyPageSize =
        typeof persisted.pageSize === 'number' && Number.isFinite(persisted.pageSize)
          ? clampCardPageSize(persisted.pageSize)
          : null;
      const regularPageSize =
        typeof persisted.regularPageSize === 'number' && Number.isFinite(persisted.regularPageSize)
          ? clampCardPageSize(persisted.regularPageSize)
          : (legacyPageSize ?? DEFAULT_REGULAR_PAGE_SIZE);
      const compactPageSize =
        typeof persisted.compactPageSize === 'number' && Number.isFinite(persisted.compactPageSize)
          ? clampCardPageSize(persisted.compactPageSize)
          : (legacyPageSize ?? DEFAULT_COMPACT_PAGE_SIZE);
      setPageSizeByMode({
        regular: regularPageSize,
        compact: compactPageSize,
      });
      if (isAuthFilesSortMode(persisted.sortMode)) {
        setSortMode(persisted.sortMode === 'default' ? 'quota' : persisted.sortMode);
      }
    }

    setUiStateHydrated(true);
  }, []);

  useEffect(() => {
    if (!uiStateHydrated) return;

    writeAuthFilesUiState({
      filter,
      problemOnly,
      disabledOnly,
      hideUsageLimited,
      compactMode,
      search,
      page,
      pageSize,
      regularPageSize: pageSizeByMode.regular,
      compactPageSize: pageSizeByMode.compact,
      sortMode,
    });
    writePersistedAuthFilesCompactMode(compactMode);
  }, [
    compactMode,
    disabledOnly,
    hideUsageLimited,
    filter,
    page,
    pageSize,
    pageSizeByMode,
    problemOnly,
    search,
    sortMode,
    uiStateHydrated,
  ]);

  useEffect(() => {
    setPageSizeInput(String(pageSize));
  }, [pageSize]);

  const setCurrentModePageSize = useCallback(
    (next: number) => {
      setPageSizeByMode((current) =>
        compactMode ? { ...current, compact: next } : { ...current, regular: next }
      );
    },
    [compactMode]
  );

  const commitPageSizeInput = (rawValue: string) => {
    const trimmed = rawValue.trim();
    if (!trimmed) {
      setPageSizeInput(String(pageSize));
      return;
    }

    const value = Number(trimmed);
    if (!Number.isFinite(value)) {
      setPageSizeInput(String(pageSize));
      return;
    }

    const next = clampCardPageSize(value);
    setCurrentModePageSize(next);
    setPageSizeInput(String(next));
    setPage(1);
  };

  const handlePageSizeChange = (event: ChangeEvent<HTMLInputElement>) => {
    const rawValue = event.currentTarget.value;
    setPageSizeInput(rawValue);

    const trimmed = rawValue.trim();
    if (!trimmed) return;

    const parsed = Number(trimmed);
    if (!Number.isFinite(parsed)) return;

    const rounded = Math.round(parsed);
    if (rounded < MIN_CARD_PAGE_SIZE || rounded > MAX_CARD_PAGE_SIZE) return;

    setCurrentModePageSize(rounded);
    setPage(1);
  };

  const handleSortModeChange = useCallback(
    (value: string) => {
      if (!isAuthFilesSortMode(value) || value === sortMode) return;
      setSortMode(value);
      setPage(1);
      void loadFiles().catch(() => {});
    },
    [loadFiles, sortMode]
  );

  const handleHeaderRefresh = useCallback(async () => {
    await Promise.all([loadFiles(), loadExcluded(), loadModelAlias()]);
  }, [loadFiles, loadExcluded, loadModelAlias]);

  useHeaderRefresh(handleHeaderRefresh);

  useEffect(() => {
    if (!isCurrentLayer) return;
    loadFiles();
    loadExcluded();
    loadModelAlias();
  }, [isCurrentLayer, loadFiles, loadExcluded, loadModelAlias]);

  useInterval(
    () => {
      void loadFiles({ silent: true }).catch(() => {});
    },
    isCurrentLayer ? AUTH_FILES_STATUS_REFRESH_INTERVAL_MS : null
  );

  const existingTypes = useMemo(() => {
    const types = new Set<string>(['all']);
    files.forEach((file) => {
      if (file.type) {
        types.add(file.type);
      }
    });
    return Array.from(types);
  }, [files]);

  const filesMatchingStatusFilters = useMemo(
    () =>
      files.filter((file) => {
        if (
          hideUsageLimited &&
          (isUsageLimitReachedAuthFile(file) || isAuthFileQuotaUsageLimited(file, quotaMaps, t))
        ) {
          return false;
        }
        if (
          problemOnly &&
          !hasAuthFileProblemStatus(file) &&
          !hasAuthFileQuotaProblem(file, quotaMaps, t)
        ) {
          return false;
        }
        if (disabledOnly && file.disabled !== true) return false;
        return true;
      }),
    [disabledOnly, files, hideUsageLimited, problemOnly, quotaMaps, t]
  );

  const sortOptions = useMemo(
    () => [
      { value: 'default', label: t('auth_files.sort_default') },
      { value: 'az', label: t('auth_files.sort_az') },
      { value: 'quota', label: t('auth_files.sort_quota') },
      { value: 'priority', label: t('auth_files.sort_priority') },
    ],
    [t]
  );

  const typeCounts = useMemo(() => {
    const counts: Record<string, number> = { all: filesMatchingStatusFilters.length };
    filesMatchingStatusFilters.forEach((file) => {
      if (!file.type) return;
      counts[file.type] = (counts[file.type] || 0) + 1;
    });
    return counts;
  }, [filesMatchingStatusFilters]);

  const quotaRefreshSummary = useMemo<QuotaRefreshSummary>(() => {
    let lastRefreshAt: number | null = null;
    let success = 0;
    let failed = 0;

    files.forEach((file) => {
      const provider = resolveAuthProvider(file);
      if (!QUOTA_PROVIDER_TYPES.has(provider as QuotaProviderType)) return;

      const snapshot = quotaSnapshotFromFile(file);
      if (!snapshot) return;

      if (snapshot.status === 'success') {
        success += 1;
      } else if (snapshot.status === 'error') {
        failed += 1;
      }

      const refreshedAt = parseQuotaUpdatedAt(snapshot.updated_at);
      if (refreshedAt !== null && (lastRefreshAt === null || refreshedAt > lastRefreshAt)) {
        lastRefreshAt = refreshedAt;
      }
    });

    return {
      failed,
      lastRefreshAt,
      nextRefreshAt: lastRefreshAt === null ? null : lastRefreshAt + QUOTA_REFRESH_INTERVAL_MS,
      success,
    };
  }, [files]);

  useInterval(
    () => {
      setQuotaRefreshNowMs(Date.now());
    },
    isCurrentLayer && quotaRefreshSummary.nextRefreshAt !== null ? 1000 : null
  );

  const normalizedSearch = search.trim();
  const wildcardSearch = useMemo(() => buildWildcardSearch(normalizedSearch), [normalizedSearch]);
  const useFilteredResultDeleteCopy =
    disabledOnly || hideUsageLimited || normalizedSearch.length > 0;

  const filtered = useMemo(() => {
    const normalizedTerm = normalizedSearch.toLowerCase();

    return filesMatchingStatusFilters.filter((item) => {
      const matchType = filter === 'all' || item.type === filter;
      const matchSearch =
        !normalizedSearch ||
        [item.name, item.type, item.provider].some((value) => {
          const content = (value || '').toString();
          return wildcardSearch
            ? wildcardSearch.test(content)
            : content.toLowerCase().includes(normalizedTerm);
        });
      return matchType && matchSearch;
    });
  }, [filesMatchingStatusFilters, filter, normalizedSearch, wildcardSearch]);

  const sorted = useMemo(() => {
    const copy = [...filtered];
    if (sortMode === 'default') {
      copy.sort((a, b) => {
        const providerA = normalizeProviderKey(String(a.provider ?? a.type ?? 'unknown'));
        const providerB = normalizeProviderKey(String(b.provider ?? b.type ?? 'unknown'));
        const providerCompare = providerA.localeCompare(providerB);
        if (providerCompare !== 0) return providerCompare;
        return a.name.localeCompare(b.name);
      });
    } else if (sortMode === 'az' || (sortMode === 'quota' && !isGroupedView)) {
      copy.sort((a, b) => a.name.localeCompare(b.name));
    } else if (sortMode === 'quota' && isGroupedView) {
      const scoreByName = new Map<string, number | null>();
      const getScore = (file: AuthFileItem) => {
        if (scoreByName.has(file.name)) return scoreByName.get(file.name) ?? null;
        const effective =
          resolveAuthProvider(file) === 'codex'
            ? (getQuotaStateForAuthFile(file, quotaMaps, t) as CodexQuotaState | undefined)
            : undefined;
        const score = getCodexRemainingQuotaScore(file, effective);
        scoreByName.set(file.name, score);
        return score;
      };

      copy.sort((a, b) => {
        const scoreA = getScore(a);
        const scoreB = getScore(b);
        const knownA = scoreA !== null;
        const knownB = scoreB !== null;
        if (knownA !== knownB) return knownA ? -1 : 1;
        if (scoreA !== null && scoreB !== null) {
          const scoreDiff = scoreB - scoreA;
          if (scoreDiff !== 0) return scoreDiff;
        }
        return a.name.localeCompare(b.name);
      });
    } else if (sortMode === 'priority') {
      copy.sort((a, b) => {
        const pa = parsePriorityValue(a.priority ?? a['priority']) ?? 0;
        const pb = parsePriorityValue(b.priority ?? b['priority']) ?? 0;
        return pb - pa;
      });
    }
    return copy;
  }, [filtered, isGroupedView, quotaMaps, sortMode, t]);

  const totalPages = Math.max(1, Math.ceil(sorted.length / pageSize));
  const currentPage = Math.min(page, totalPages);
  const start = (currentPage - 1) * pageSize;
  const pageItems = isGroupedView ? sorted : sorted.slice(start, start + pageSize);
  const codexQuotaForGrouping = useMemo(() => {
    if (!isGroupedView) return {};
    const quotaByName: Record<string, CodexQuotaState> = {};
    pageItems.forEach((file) => {
      if (resolveAuthProvider(file) !== 'codex') return;
      const effective = getQuotaStateForAuthFile(file, quotaMaps, t) as CodexQuotaState | undefined;
      if (effective) {
        quotaByName[file.name] = effective;
      }
    });
    return quotaByName;
  }, [isGroupedView, pageItems, quotaMaps, t]);
  const providerGroups = useMemo<ProviderFileGroup[]>(() => {
    if (!isAllGroupedView) return [];

    const groups = new Map<string, ProviderFileGroup>();
    pageItems.forEach((file) => {
      const providerKey = normalizeProviderKey(resolveAuthProvider(file) || file.type || 'unknown');
      const existing = groups.get(providerKey);
      if (existing) {
        existing.files.push(file);
        return;
      }

      groups.set(providerKey, {
        key: providerKey,
        label: getTypeLabel(t, providerKey),
        files: [file],
      });
    });

    return Array.from(groups.values()).sort((a, b) => a.label.localeCompare(b.label));
  }, [isAllGroupedView, pageItems, t]);
  const selectablePageItems = useMemo(
    () => pageItems.filter((file) => !isRuntimeOnlyAuthFile(file)),
    [pageItems]
  );
  const selectableFilteredItems = useMemo(
    () => sorted.filter((file) => !isRuntimeOnlyAuthFile(file)),
    [sorted]
  );
  const selectedNames = useMemo(() => Array.from(selectedFiles), [selectedFiles]);
  const selectedHasStatusUpdating = useMemo(
    () => selectedNames.some((name) => statusUpdating[name] === true),
    [selectedNames, statusUpdating]
  );
  const batchStatusButtonsDisabled =
    disableControls ||
    selectedNames.length === 0 ||
    batchStatusUpdating ||
    selectedHasStatusUpdating;

  const copyTextWithNotification = useCallback(
    async (text: string) => {
      const copied = await copyToClipboard(text);
      showNotification(
        copied
          ? t('notification.link_copied', { defaultValue: 'Copied to clipboard' })
          : t('notification.copy_failed', { defaultValue: 'Copy failed' }),
        copied ? 'success' : 'error'
      );
    },
    [showNotification, t]
  );

  const openExcludedEditor = useCallback(
    (provider?: string) => {
      const providerValue = (provider || (filter !== 'all' ? String(filter) : '')).trim();
      const params = new URLSearchParams();
      if (providerValue) {
        params.set('provider', providerValue);
      }
      const nextSearch = params.toString();
      navigate(`/auth-files/oauth-excluded${nextSearch ? `?${nextSearch}` : ''}`, {
        state: { fromAuthFiles: true },
      });
    },
    [filter, navigate]
  );

  const openModelAliasEditor = useCallback(
    (provider?: string) => {
      const providerValue = (provider || (filter !== 'all' ? String(filter) : '')).trim();
      const params = new URLSearchParams();
      if (providerValue) {
        params.set('provider', providerValue);
      }
      const nextSearch = params.toString();
      navigate(`/auth-files/oauth-model-alias${nextSearch ? `?${nextSearch}` : ''}`, {
        state: { fromAuthFiles: true },
      });
    },
    [filter, navigate]
  );

  useLayoutEffect(() => {
    if (typeof window === 'undefined') return;

    const actionsEl = floatingBatchActionsRef.current;
    if (!actionsEl) {
      document.documentElement.style.removeProperty('--auth-files-action-bar-height');
      return;
    }

    const updatePadding = () => {
      const height = actionsEl.getBoundingClientRect().height;
      document.documentElement.style.setProperty('--auth-files-action-bar-height', `${height}px`);
    };

    updatePadding();
    window.addEventListener('resize', updatePadding);

    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(updatePadding);
    ro?.observe(actionsEl);

    return () => {
      ro?.disconnect();
      window.removeEventListener('resize', updatePadding);
      document.documentElement.style.removeProperty('--auth-files-action-bar-height');
    };
  }, [batchActionBarVisible, selectionCount]);

  useEffect(() => {
    selectionCountRef.current = selectionCount;
    if (selectionCount > 0) {
      setBatchActionBarVisible(true);
    }
  }, [selectionCount]);

  useLayoutEffect(() => {
    if (!batchActionBarVisible) return;
    const currentCount = selectionCount;
    const previousCount = previousSelectionCountRef.current;
    const actionsEl = floatingBatchActionsRef.current;
    if (!actionsEl) return;

    batchActionAnimationRef.current?.stop();
    batchActionAnimationRef.current = null;

    if (currentCount > 0 && previousCount === 0) {
      batchActionAnimationRef.current = animate(
        actionsEl,
        {
          transform: [BATCH_BAR_HIDDEN_TRANSFORM, BATCH_BAR_BASE_TRANSFORM],
          opacity: [0, 1],
        },
        {
          duration: 0.28,
          ease: easePower3Out,
          onComplete: () => {
            actionsEl.style.transform = BATCH_BAR_BASE_TRANSFORM;
            actionsEl.style.opacity = '1';
          },
        }
      );
    } else if (currentCount === 0 && previousCount > 0) {
      batchActionAnimationRef.current = animate(
        actionsEl,
        {
          transform: [BATCH_BAR_BASE_TRANSFORM, BATCH_BAR_HIDDEN_TRANSFORM],
          opacity: [1, 0],
        },
        {
          duration: 0.22,
          ease: easePower2In,
          onComplete: () => {
            if (selectionCountRef.current === 0) {
              setBatchActionBarVisible(false);
            }
          },
        }
      );
    }

    previousSelectionCountRef.current = currentCount;
  }, [batchActionBarVisible, selectionCount]);

  useEffect(
    () => () => {
      batchActionAnimationRef.current?.stop();
      batchActionAnimationRef.current = null;
    },
    []
  );

  const renderFilterTags = () => (
    <div className={styles.filterRail}>
      <div className={styles.filterTags}>
        {existingTypes.map((type) => {
          const isActive = filter === type;
          const iconSrc = getAuthFileIcon(type, resolvedTheme);
          const color =
            type === 'all'
              ? { bg: 'var(--bg-tertiary)', text: 'var(--text-primary)' }
              : getTypeColor(type, resolvedTheme);
          const buttonStyle = {
            '--filter-color': color.text,
            '--filter-surface': color.bg,
            '--filter-active-text': resolvedTheme === 'dark' ? '#111827' : '#ffffff',
          } as CSSProperties;

          return (
            <button
              key={type}
              className={`${styles.filterTag} ${isActive ? styles.filterTagActive : ''}`}
              style={buttonStyle}
              onClick={() => {
                setFilter(type);
                setPage(1);
              }}
            >
              <span className={styles.filterTagLabel}>
                {type === 'all' ? (
                  <span className={`${styles.filterTagIconWrap} ${styles.filterAllIconWrap}`}>
                    <IconFilterAll className={styles.filterAllIcon} size={16} />
                  </span>
                ) : (
                  <span className={styles.filterTagIconWrap}>
                    {iconSrc ? (
                      <img src={iconSrc} alt="" className={styles.filterTagIcon} />
                    ) : (
                      <span className={styles.filterTagIconFallback}>
                        {getTypeLabel(t, type).slice(0, 1).toUpperCase()}
                      </span>
                    )}
                  </span>
                )}
                <span className={styles.filterTagText}>{getTypeLabel(t, type)}</span>
              </span>
              <span className={styles.filterTagCount}>{typeCounts[type] ?? 0}</span>
            </button>
          );
        })}
      </div>
    </div>
  );

  const deleteAllButtonLabel = (() => {
    if (useFilteredResultDeleteCopy) {
      return t('auth_files.delete_filtered_result_button');
    }
    if (problemOnly) {
      return filter === 'all'
        ? t('auth_files.delete_problem_button')
        : t('auth_files.delete_problem_button_with_type', { type: getTypeLabel(t, filter) });
    }
    return filter === 'all'
      ? t('auth_files.delete_all_button')
      : `${t('common.delete')} ${getTypeLabel(t, filter)}`;
  })();

  const renderAuthFileCard = (file: AuthFileItem) => (
    <AuthFileCard
      key={file.name}
      file={file}
      compact={compactMode}
      selected={selectedFiles.has(file.name)}
      resolvedTheme={resolvedTheme}
      disableControls={disableControls}
      deleting={deleting}
      statusUpdating={statusUpdating}
      quotaFilterType={quotaFilterType}
      statusBarCache={statusBarCache}
      onShowModels={showModels}
      onShowDetails={setDetailsFile}
      onDownload={handleDownload}
      onOpenPrefixProxyEditor={openPrefixProxyEditor}
      onDelete={handleDelete}
      onToggleStatus={handleStatusToggle}
      onToggleSelect={toggleSelect}
    />
  );

  const renderAuthFileGrid = (items: AuthFileItem[], quotaManaged = false) => (
    <div
      className={`${styles.fileGrid} ${quotaManaged ? styles.fileGridQuotaManaged : ''} ${compactMode ? styles.fileGridCompact : ''}`}
    >
      {items.map(renderAuthFileCard)}
    </div>
  );

  const renderCodexFileGroups = (items: AuthFileItem[]) => (
    <div className={styles.fileGroups}>
      {(CODEX_CONFIG.groupFiles?.(items, codexQuotaForGrouping, t) ?? []).map((group) => (
        <section key={group.key} className={styles.fileGroup}>
          <h3 className={styles.fileGroupTitle}>
            <span>{group.label}</span>
            <span className={styles.fileGroupCount}>{group.files.length}</span>
          </h3>
          {renderAuthFileGrid(group.files, true)}
        </section>
      ))}
    </div>
  );

  const renderProviderGroup = (group: ProviderFileGroup) => {
    const iconSrc = getAuthFileIcon(group.key, resolvedTheme);
    const color = getTypeColor(group.key, resolvedTheme);
    const quotaManaged = QUOTA_PROVIDER_TYPES.has(group.key as QuotaProviderType);

    return (
      <section key={group.key} className={styles.providerGroup}>
        <header className={styles.providerGroupHeader}>
          <div className={styles.providerGroupTitle}>
            <span
              className={styles.providerGroupIconWrap}
              style={{
                backgroundColor: color.bg,
                color: color.text,
                ...(color.border ? { border: color.border } : {}),
              }}
            >
              {iconSrc ? (
                <img src={iconSrc} alt="" className={styles.providerGroupIcon} />
              ) : (
                <span className={styles.providerGroupIconFallback}>
                  {group.label.slice(0, 1).toUpperCase()}
                </span>
              )}
            </span>
            <span>{group.label}</span>
            <span className={styles.providerGroupCount}>{group.files.length}</span>
          </div>
        </header>
        <div className={styles.providerGroupBody}>
          {group.key === 'codex'
            ? renderCodexFileGroups(group.files)
            : renderAuthFileGrid(group.files, quotaManaged)}
        </div>
      </section>
    );
  };

  return (
    <div className={styles.container}>
      <div className={styles.pageHeader}>
        <h1 className={styles.pageTitle}>{t('auth_files.title')}</h1>
        <p className={styles.description}>{t('auth_files.description')}</p>
      </div>

      <Card
        title={
          <div className={styles.quotaRefreshSummary}>
            <div className={styles.quotaRefreshTile}>
              <span>{t('auth_files.quota_refresh_last')}</span>
              <strong>
                {quotaRefreshSummary.lastRefreshAt === null
                  ? t('auth_files.refresh_not_available')
                  : formatClockTime(quotaRefreshSummary.lastRefreshAt)}
              </strong>
            </div>
            <div className={styles.quotaRefreshTile}>
              <span>{t('auth_files.quota_refresh_next')}</span>
              <strong className={styles.quotaRefreshCountdown}>
                {quotaRefreshSummary.nextRefreshAt === null
                  ? t('auth_files.refresh_not_available')
                  : formatCountdown(quotaRefreshSummary.nextRefreshAt - quotaRefreshNowMs)}
              </strong>
            </div>
            <div className={styles.quotaRefreshTile}>
              <span>{t('auth_files.quota_refresh_result')}</span>
              <strong>
                {quotaRefreshSummary.success} {t('common.success')} · {quotaRefreshSummary.failed}{' '}
                {t('common.failure')}
              </strong>
            </div>
          </div>
        }
        extra={
          <div className={styles.headerActions}>
            <Button
              variant="secondary"
              size="sm"
              onClick={handleHeaderRefresh}
              disabled={filesBusy}
              loading={refreshing}
            >
              {t('common.refresh')}
            </Button>
            <Button
              size="sm"
              onClick={handleUploadClick}
              disabled={disableControls || uploading}
              loading={uploading}
            >
              {t('auth_files.upload_button')}
            </Button>
            <Button
              variant="danger"
              size="sm"
              onClick={() =>
                handleDeleteAll({
                  filter,
                  problemOnly,
                  disabledOnly,
                  visibleFileNames: sorted.map((file) => file.name),
                  useFilteredResultCopy: useFilteredResultDeleteCopy,
                  onResetFilterToAll: () => setFilter('all'),
                  onResetProblemOnly: () => setProblemOnly(false),
                  onResetDisabledOnly: () => setDisabledOnly(false),
                })
              }
              disabled={disableControls || filesBusy || deletingAll}
              loading={deletingAll}
            >
              {deleteAllButtonLabel}
            </Button>
            <input
              ref={fileInputRef}
              type="file"
              accept=".json,application/json"
              multiple
              style={{ display: 'none' }}
              onChange={handleFileChange}
            />
          </div>
        }
      >
        {error && <div className={styles.errorBox}>{error}</div>}

        <div className={styles.filterSection}>
          {renderFilterTags()}

          <div className={styles.filterContent}>
            <div className={styles.filterControlsPanel}>
              <div className={styles.filterControls}>
                <div className={styles.filterItem}>
                  <label>{t('auth_files.search_label')}</label>
                  <Input
                    value={search}
                    onChange={(e) => {
                      setSearch(e.target.value);
                      setPage(1);
                    }}
                    placeholder={t('auth_files.search_placeholder')}
                  />
                </div>
                {!isGroupedView && (
                  <div className={styles.filterItem}>
                    <label>{t('auth_files.page_size_label')}</label>
                    <input
                      className={styles.pageSizeSelect}
                      type="number"
                      min={MIN_CARD_PAGE_SIZE}
                      max={MAX_CARD_PAGE_SIZE}
                      step={1}
                      value={pageSizeInput}
                      onChange={handlePageSizeChange}
                      onBlur={(e) => commitPageSizeInput(e.currentTarget.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.currentTarget.blur();
                        }
                      }}
                    />
                  </div>
                )}
                <div className={styles.filterItem}>
                  <label>{t('auth_files.sort_label')}</label>
                  <Select
                    className={styles.sortSelect}
                    value={sortMode}
                    options={sortOptions}
                    onChange={handleSortModeChange}
                    ariaLabel={t('auth_files.sort_label')}
                    fullWidth
                  />
                </div>
                <div className={`${styles.filterItem} ${styles.filterToggleItem}`}>
                  <label>{t('auth_files.display_options_label')}</label>
                  <div className={styles.filterToggleGroup}>
                    <div className={styles.filterToggleCard}>
                      <ToggleSwitch
                        checked={problemOnly}
                        onChange={(value) => {
                          setProblemOnly(value);
                          setPage(1);
                        }}
                        ariaLabel={t('auth_files.problem_filter_only')}
                        label={
                          <span className={styles.filterToggleLabel}>
                            <span>{t('auth_files.problem_filter_only')}</span>
                            <DisplayOptionIcon type="problem" />
                          </span>
                        }
                      />
                    </div>
                    <div className={styles.filterToggleCard}>
                      <ToggleSwitch
                        checked={disabledOnly}
                        onChange={(value) => {
                          setDisabledOnly(value);
                          setPage(1);
                        }}
                        ariaLabel={t('auth_files.disabled_filter_only')}
                        label={
                          <span className={styles.filterToggleLabel}>
                            <span>{t('auth_files.disabled_filter_only')}</span>
                            <DisplayOptionIcon type="disabled" />
                          </span>
                        }
                      />
                    </div>
                    <div className={styles.filterToggleCard}>
                      <ToggleSwitch
                        checked={hideUsageLimited}
                        onChange={(value) => {
                          setHideUsageLimited(value);
                          setPage(1);
                        }}
                        ariaLabel={t('auth_files.hide_usage_limited')}
                        label={
                          <span className={styles.filterToggleLabel}>
                            <span>{t('auth_files.hide_usage_limited')}</span>
                            <DisplayOptionIcon type="quota" />
                          </span>
                        }
                      />
                    </div>
                    <div className={styles.filterToggleCard}>
                      <ToggleSwitch
                        checked={compactMode}
                        onChange={(value) => setCompactMode(value)}
                        ariaLabel={t('auth_files.compact_mode_label')}
                        label={
                          <span className={styles.filterToggleLabel}>
                            <span>{t('auth_files.compact_mode_label')}</span>
                            <DisplayOptionIcon type="compact" />
                          </span>
                        }
                      />
                    </div>
                  </div>
                </div>
              </div>
            </div>

            {loading ? (
              <div className={styles.hint}>{t('common.loading')}</div>
            ) : pageItems.length === 0 ? (
              <EmptyState
                title={t('auth_files.search_empty_title')}
                description={t('auth_files.search_empty_desc')}
              />
            ) : isAllGroupedView ? (
              <div className={styles.providerGroups}>{providerGroups.map(renderProviderGroup)}</div>
            ) : isCodexGroupedView ? (
              renderCodexFileGroups(pageItems)
            ) : (
              renderAuthFileGrid(pageItems, Boolean(quotaFilterType))
            )}

            {!loading && !isGroupedView && sorted.length > pageSize && (
              <div className={styles.pagination}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setPage(Math.max(1, currentPage - 1))}
                  disabled={currentPage <= 1}
                >
                  {t('auth_files.pagination_prev')}
                </Button>
                <div className={styles.pageInfo}>
                  {t('auth_files.pagination_info', {
                    current: currentPage,
                    total: totalPages,
                    count: sorted.length,
                  })}
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setPage(Math.min(totalPages, currentPage + 1))}
                  disabled={currentPage >= totalPages}
                >
                  {t('auth_files.pagination_next')}
                </Button>
              </div>
            )}
          </div>
        </div>
      </Card>

      <OAuthExcludedCard
        disableControls={disableControls}
        excludedError={excludedError}
        excluded={excluded}
        onAdd={() => openExcludedEditor()}
        onEdit={openExcludedEditor}
        onDelete={deleteExcluded}
      />

      <OAuthModelAliasCard
        disableControls={disableControls}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        onAdd={() => openModelAliasEditor()}
        onEditProvider={openModelAliasEditor}
        onDeleteProvider={deleteModelAlias}
        modelAliasError={modelAliasError}
        modelAlias={modelAlias}
        allProviderModels={allProviderModels}
        onUpdate={handleMappingUpdate}
        onDeleteLink={handleDeleteLink}
        onToggleFork={handleToggleFork}
        onRenameAlias={handleRenameAlias}
        onDeleteAlias={handleDeleteAlias}
      />

      <AuthFileModelsModal
        open={modelsModalOpen}
        fileName={modelsFileName}
        fileType={modelsFileType}
        loading={modelsLoading}
        error={modelsError}
        models={modelsList}
        excluded={excluded}
        onClose={closeModelsModal}
        onCopyText={copyTextWithNotification}
      />

      <AuthFileDetailsModal
        file={detailsFile}
        onClose={() => setDetailsFile(null)}
        onCopyText={copyTextWithNotification}
      />

      <AuthFilesPrefixProxyEditorModal
        disableControls={disableControls}
        editor={prefixProxyEditor}
        updatedText={prefixProxyUpdatedText}
        dirty={prefixProxyDirty}
        onClose={closePrefixProxyEditor}
        onCopyText={copyTextWithNotification}
        onSave={handlePrefixProxySave}
        onChange={handlePrefixProxyChange}
      />

      {batchActionBarVisible && typeof document !== 'undefined'
        ? createPortal(
            <div className={styles.batchActionContainer} ref={floatingBatchActionsRef}>
              <div className={styles.batchActionBar}>
                <div className={styles.batchActionLeft}>
                  <span className={styles.batchSelectionText}>
                    {t('auth_files.batch_selected', { count: selectionCount })}
                  </span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => selectAllVisible(pageItems)}
                    disabled={selectablePageItems.length === 0}
                  >
                    {t('auth_files.batch_select_page')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => selectAllVisible(sorted)}
                    disabled={selectableFilteredItems.length === 0}
                  >
                    {t('auth_files.batch_select_filtered')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => invertVisibleSelection(pageItems)}
                    disabled={selectablePageItems.length === 0}
                  >
                    {t('auth_files.batch_invert_page')}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={deselectAll}>
                    {t('auth_files.batch_deselect')}
                  </Button>
                </div>
                <div className={styles.batchActionRight}>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void batchDownload(selectedNames)}
                    disabled={disableControls || selectedNames.length === 0}
                  >
                    {t('auth_files.batch_download')}
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => batchSetStatus(selectedNames, true)}
                    disabled={batchStatusButtonsDisabled}
                  >
                    {t('auth_files.batch_enable')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => batchSetStatus(selectedNames, false)}
                    disabled={batchStatusButtonsDisabled}
                  >
                    {t('auth_files.batch_disable')}
                  </Button>
                  <Button
                    variant="danger"
                    size="sm"
                    onClick={() => batchDelete(selectedNames)}
                    disabled={disableControls || selectedNames.length === 0}
                  >
                    {t('common.delete')}
                  </Button>
                </div>
              </div>
            </div>,
            document.body
          )
        : null}
    </div>
  );
}

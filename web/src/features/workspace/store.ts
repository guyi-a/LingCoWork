import { create } from "zustand";
import { persist } from "zustand/middleware";

export type WorkspaceTab = "files" | "diff" | "problems" | "terminal";
export type WorkspaceDiffScope = "agent" | "all";

interface State {
  panelOpen: boolean;
  previewPath: string | null;
  previewLine: number | null;
  previewWidth: number;
  expandedDirectories: Record<string, true>;
  switcherOpen: boolean;
  filesVersion: number;
  activeTab: WorkspaceTab;
  diffScope: WorkspaceDiffScope;
  selectedDiffPath: string | null;
  setPanelOpen: (open: boolean) => void;
  togglePanel: () => void;
  openFile: (path: string, line?: number) => void;
  closePreview: () => void;
  resetConversationState: () => void;
  setPreviewWidth: (width: number) => void;
  toggleDirectory: (key: string) => void;
  toggleSwitcher: () => void;
  closeSwitcher: () => void;
  refreshFiles: () => void;
  setActiveTab: (tab: WorkspaceTab) => void;
  setDiffScope: (scope: WorkspaceDiffScope) => void;
  selectDiffPath: (path: string | null) => void;
}

const PREVIEW_MIN_WIDTH = 360;
const PREVIEW_MAX_WIDTH = 760;

// Coalesce refreshFiles calls: a burst of tool results (each of which bumps
// the tree) within a short window becomes a single filesVersion bump, so the
// workspace tree doesn't refetch + the panel doesn't re-render on every single
// tool event during a run. That churn is what made the panel visibly flash.
const REFRESH_THROTTLE_MS = 600;
let lastRefreshAt = 0;

export const useWorkspaceStore = create<State>()(
  persist(
    (set) => ({
      panelOpen: false,
      previewPath: null,
      previewLine: null,
      previewWidth: 520,
      expandedDirectories: {},
      switcherOpen: false,
      filesVersion: 0,
      activeTab: "files",
      diffScope: "agent",
      selectedDiffPath: null,
      setPanelOpen: (open) => set({ panelOpen: open }),
      togglePanel: () => set((s) => ({ panelOpen: !s.panelOpen })),
      openFile: (path, line) =>
        set({
          panelOpen: true,
          activeTab: "files",
          previewPath: path,
          previewLine: line && line > 0 ? line : null,
          switcherOpen: false,
          selectedDiffPath: null,
        }),
      closePreview: () =>
        set({ previewPath: null, previewLine: null, switcherOpen: false }),
      resetConversationState: () =>
        set((s) => ({
          previewPath: null,
          previewLine: null,
          switcherOpen: false,
          selectedDiffPath: null,
          filesVersion: s.filesVersion + 1,
        })),
      setPreviewWidth: (width) =>
        set({
          previewWidth: Math.max(
            PREVIEW_MIN_WIDTH,
            Math.min(PREVIEW_MAX_WIDTH, width),
          ),
      }),
      toggleDirectory: (key) =>
        set((s) => {
          const expandedDirectories = { ...s.expandedDirectories };
          if (expandedDirectories[key]) {
            delete expandedDirectories[key];
          } else {
            expandedDirectories[key] = true;
          }
          return { expandedDirectories };
        }),
      toggleSwitcher: () => set((s) => ({ switcherOpen: !s.switcherOpen })),
      closeSwitcher: () => set({ switcherOpen: false }),
      refreshFiles: () => {
        const now = Date.now();
        if (now - lastRefreshAt < REFRESH_THROTTLE_MS) return;
        lastRefreshAt = now;
        set((s) => ({ filesVersion: s.filesVersion + 1 }));
      },
      setActiveTab: (activeTab) => set({ activeTab, switcherOpen: false }),
      setDiffScope: (diffScope) =>
        set({ diffScope, selectedDiffPath: null, switcherOpen: false }),
      selectDiffPath: (selectedDiffPath) =>
        set({
          panelOpen: true,
          activeTab: "diff",
          selectedDiffPath,
          switcherOpen: false,
        }),
    }),
    {
      name: "workspace-panel",
      partialize: (s) => ({
        panelOpen: s.panelOpen,
        previewWidth: s.previewWidth,
        expandedDirectories: s.expandedDirectories,
        activeTab: s.activeTab,
        diffScope: s.diffScope,
      }),
    },
  ),
);

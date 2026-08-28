import { create } from "zustand";
import { persist } from "zustand/middleware";

export type WorkspaceTab = "files" | "diff" | "terminal";
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
      refreshFiles: () => set((s) => ({ filesVersion: s.filesVersion + 1 })),
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

import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { useParams } from "react-router";
import { useWorkspaceStore } from "./store";
import { WorkspaceTree } from "./WorkspaceTree";
import { FilePreview } from "./FilePreview";
import { cn } from "@/lib/utils";
import { WorkspaceDiffPanel } from "./WorkspaceDiff";
import { WorkspaceProblems } from "./WorkspaceProblems";
import type { WorkspaceTab } from "./store";
import { useProblemsStore } from "./problems-store";

const WorkspaceTerminal = lazy(() =>
  import("./WorkspaceTerminal").then((module) => ({
    default: module.WorkspaceTerminal,
  })),
);

export function WorkspacePanel({
  streaming,
  projectId,
}: {
  streaming: boolean;
  projectId?: string;
}) {
  const { id: conversationId } = useParams();
  const panelOpen = useWorkspaceStore((s) => s.panelOpen);
  const previewPath = useWorkspaceStore((s) => s.previewPath);
  const previewWidth = useWorkspaceStore((s) => s.previewWidth);
  const setPreviewWidth = useWorkspaceStore((s) => s.setPreviewWidth);
  const refreshFiles = useWorkspaceStore((s) => s.refreshFiles);
  const filesVersion = useWorkspaceStore((s) => s.filesVersion);
  const activeTab = useWorkspaceStore((s) => s.activeTab);
  const setActiveTab = useWorkspaceStore((s) => s.setActiveTab);
  const resetConversationState = useWorkspaceStore(
    (s) => s.resetConversationState,
  );
  const startXRef = useRef(0);
  const startWidthRef = useRef(previewWidth);
  const previousConversationIdRef = useRef<string | undefined>(conversationId);
  const [resizing, setResizing] = useState(false);
  const [terminalMounted, setTerminalMounted] = useState(
    panelOpen && activeTab === "terminal",
  );
  const problemCount = useProblemsStore(
    (s) => s.data[conversationId ?? ""]?.error_count ?? 0,
  );
  const clearProblems = useProblemsStore((s) => s.clear);
  const loadProblems = useProblemsStore((s) => s.load);

  useEffect(() => {
    if (panelOpen && activeTab === "terminal") setTerminalMounted(true);
  }, [activeTab, panelOpen]);

  useEffect(() => {
    if (!conversationId) return;
    const controller = new AbortController();
    void loadProblems(conversationId, controller.signal);
    return () => controller.abort();
  }, [conversationId, filesVersion, loadProblems]);

  useEffect(() => {
    if (!conversationId) return;
    if (previousConversationIdRef.current === conversationId) return;
    if (previousConversationIdRef.current) {
      clearProblems(previousConversationIdRef.current);
    }
    previousConversationIdRef.current = conversationId;
    resetConversationState();
  }, [clearProblems, conversationId, resetConversationState]);

  const onPointerDown = useCallback(
    (event: React.PointerEvent) => {
      event.preventDefault();
      startXRef.current = event.clientX;
      startWidthRef.current = previewWidth;
      setResizing(true);
    },
    [previewWidth],
  );

  useEffect(() => {
    if (!resizing) return;

    const onPointerMove = (event: PointerEvent) => {
      const delta = startXRef.current - event.clientX;
      setPreviewWidth(startWidthRef.current + delta);
    };
    const onPointerUp = () => setResizing(false);

    window.addEventListener("pointermove", onPointerMove);
    window.addEventListener("pointerup", onPointerUp);
    return () => {
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerup", onPointerUp);
    };
  }, [resizing, setPreviewWidth]);

  useEffect(() => {
    // Refresh once when a run starts; further refreshes are event-driven (the
    // chat stream calls refreshFiles on file-affecting tool results, and
    // refreshFiles is throttled in the store). No fixed polling here — a
    // setInterval every 2s was triggering the workspace tree + Problems reload
    // on a timer, which under the typewriter's per-frame work showed up as the
    // panel flashing.
    if (!panelOpen || !conversationId || !streaming) return;
    refreshFiles();
  }, [conversationId, panelOpen, refreshFiles, streaming]);

  if (!conversationId) return null;

  return (
    <aside
      className={cn(
        "relative shrink-0 flex flex-col min-h-0 p-3",
        !panelOpen && "hidden",
        !resizing && "transition-[width] duration-200 ease-out",
        resizing && "select-none",
      )}
      style={{ width: previewWidth }}
    >
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="调整 Workspace 面板宽度"
        tabIndex={0}
        onPointerDown={onPointerDown}
        className={cn(
          "absolute left-0 top-0 z-20 h-full w-2 cursor-col-resize",
          "before:absolute before:left-3 before:top-3 before:bottom-3 before:w-px before:bg-rule/80",
          "after:absolute after:left-2 after:top-3 after:bottom-3 after:w-1 after:rounded-full after:bg-transparent hover:after:bg-accent/20",
        )}
      />
      <div className="flex-1 min-h-0 flex flex-col rounded-lg border border-rule bg-paper overflow-hidden">
        <WorkspaceTabs
          active={activeTab}
          onChange={setActiveTab}
          problemCount={problemCount}
        />
        {activeTab === "files" &&
          (previewPath ? (
            <FilePreview
              conversationId={conversationId}
              path={previewPath}
              projectId={projectId}
            />
          ) : (
            <WorkspaceTree
              conversationId={conversationId}
              projectId={projectId}
            />
          ))}
        {activeTab === "diff" && (
          <WorkspaceDiffPanel
            conversationId={conversationId}
            projectId={projectId}
          />
        )}
        {activeTab === "problems" && (
          <WorkspaceProblems conversationId={conversationId} />
        )}
        {terminalMounted && (
          <div
            className={cn(
              "min-h-0 flex-1",
              activeTab === "terminal" ? "flex" : "hidden",
            )}
          >
            <Suspense
              fallback={
                <div className="flex flex-1 items-center justify-center bg-[#171717] font-mono text-[10px] text-white/40">
                  Loading terminal…
                </div>
              }
            >
              <WorkspaceTerminal
                conversationId={conversationId}
                projectId={projectId}
                active={panelOpen && activeTab === "terminal"}
              />
            </Suspense>
          </div>
        )}
      </div>
    </aside>
  );
}

function WorkspaceTabs({
  active,
  onChange,
  problemCount,
}: {
  active: WorkspaceTab;
  onChange: (tab: WorkspaceTab) => void;
  problemCount: number;
}) {
  const tabs: Array<{ id: WorkspaceTab; label: string }> = [
    { id: "files", label: "Files" },
    { id: "diff", label: "Diff" },
    { id: "problems", label: "Problems" },
    { id: "terminal", label: "Terminal" },
  ];
  return (
    <div className="flex h-9 shrink-0 items-end gap-1 border-b border-rule px-2">
      {tabs.map((tab) => (
        <button
          key={tab.id}
          type="button"
          onClick={() => onChange(tab.id)}
          className={cn(
            "relative h-8 px-2 text-[10px] font-mono uppercase tracking-[0.12em] transition-colors",
            active === tab.id ? "text-ink" : "text-muted hover:text-ink",
            active === tab.id &&
              "after:absolute after:inset-x-1 after:bottom-0 after:h-0.5 after:bg-accent",
          )}
        >
          {tab.label}
          {tab.id === "problems" && problemCount > 0 && (
            <span className="ml-1 rounded-full bg-red-100 px-1 text-[9px] text-red-700">
              {problemCount > 99 ? "99+" : problemCount}
            </span>
          )}
        </button>
      ))}
    </div>
  );
}

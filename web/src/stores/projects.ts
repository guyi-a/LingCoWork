import { create } from "zustand";
import {
  deleteProject,
  listProjects,
  openProject,
  openProjectInFinder,
  renameProject,
  type ProjectItem,
} from "@/lib/api";

interface ProjectStore {
  items: ProjectItem[];
  loading: boolean;
  refresh: () => Promise<void>;
  openFolder: (path: string, name?: string) => Promise<ProjectItem>;
  rename: (id: string, name: string) => Promise<void>;
  remove: (id: string) => Promise<string | undefined>; // returns warning if any
  openInFinder: (id: string) => Promise<void>;
}

export const useProjectStore = create<ProjectStore>((set, get) => ({
  items: [],
  loading: false,

  refresh: async () => {
    set({ loading: true });
    try {
      const items = await listProjects();
      set({ items, loading: false });
    } catch (err) {
      console.error("[projects] refresh failed:", err);
      set({ loading: false });
    }
  },

  openFolder: async (path, name) => {
    const { project } = await openProject(path, name);
    set((state) => ({
      items: [project, ...state.items.filter((item) => item.id !== project.id)],
    }));
    return project;
  },

  rename: async (id, name) => {
    const prev = get().items;
    set({
      items: prev.map((p) => (p.id === id ? { ...p, name } : p)),
    });
    try {
      await renameProject(id, name);
    } catch (err) {
      console.error("[projects] rename failed:", err);
      set({ items: prev });
      throw err;
    }
  },

  remove: async (id) => {
    const prev = get().items;
    set({ items: prev.filter((p) => p.id !== id) });
    try {
      const res = await deleteProject(id);
      return res.warning;
    } catch (err) {
      console.error("[projects] delete failed:", err);
      set({ items: prev });
      throw err;
    }
  },

  openInFinder: async (id) => {
    try {
      await openProjectInFinder(id);
    } catch (err) {
      console.error("[projects] open in finder failed:", err);
      throw err;
    }
  },
}));

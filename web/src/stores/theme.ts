import { create } from "zustand";

export type Theme = "light" | "dark" | "system";
export type EffectiveTheme = "light" | "dark";

export const THEME_STORAGE_KEY = "lingcowork.theme";

function readStoredTheme(): Theme {
  if (typeof localStorage === "undefined") return "system";
  const value = localStorage.getItem(THEME_STORAGE_KEY);
  return value === "light" || value === "dark" || value === "system"
    ? value
    : "system";
}

function systemPrefersDark(): boolean {
  return (
    typeof window !== "undefined" &&
    window.matchMedia("(prefers-color-scheme: dark)").matches
  );
}

export function resolveEffectiveTheme(theme: Theme): EffectiveTheme {
  if (theme === "system") return systemPrefersDark() ? "dark" : "light";
  return theme;
}

interface ThemeStore {
  theme: Theme;
  effective: EffectiveTheme;
  setTheme: (theme: Theme) => void;
  refreshEffective: () => void;
}

const initialTheme = readStoredTheme();

export const useThemeStore = create<ThemeStore>((set, get) => ({
  theme: initialTheme,
  effective: resolveEffectiveTheme(initialTheme),
  setTheme: (theme) => {
    localStorage.setItem(THEME_STORAGE_KEY, theme);
    set({ theme, effective: resolveEffectiveTheme(theme) });
    applyThemeClass(theme);
  },
  refreshEffective: () => {
    set({ effective: resolveEffectiveTheme(get().theme) });
    applyThemeClass(get().theme);
  },
}));

export function isDarkTheme(): boolean {
  return useThemeStore.getState().effective === "dark";
}

function applyThemeClass(theme: Theme): void {
  if (typeof document === "undefined") return;
  const effective = resolveEffectiveTheme(theme);
  document.documentElement.classList.toggle("dark", effective === "dark");
}

let mediaQuery: MediaQueryList | null = null;

// Call once before the first render so the `<html>` class is correct before
// React paints, avoiding a light-mode flash for dark users.
export function initTheme(): void {
  applyThemeClass(useThemeStore.getState().theme);
  if (mediaQuery || typeof window === "undefined" || !window.matchMedia) {
    return;
  }
  mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
  mediaQuery.addEventListener("change", () => {
    if (useThemeStore.getState().theme === "system") {
      useThemeStore.getState().refreshEffective();
    }
  });
}

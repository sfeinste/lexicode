/*
 * The app chrome (UI spec §2.1): 40px fixed top bar, 208px left rail, fluid content.
 * Everything inside the shell is authenticated — the gate lives here, so the auth screens
 * (which render outside this layout) stay reachable.
 */
import { useQuery } from "@tanstack/react-query";
import { Navigate, Outlet, useNavigate, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { AskAgentPalette } from "../../components/AskAgentPalette/AskAgentPalette";
import { CommandPalette } from "../../components/CommandPalette/CommandPalette";
import { KeyboardCheatsheet } from "../../components/KeyboardCheatsheet/KeyboardCheatsheet";
import { ApiProblem, authApi } from "../../lib/api/client";
import { useKeyBindings } from "../../lib/keyboard/hooks";
import type { KeyBinding } from "../../lib/keyboard/registry";
import { useUIStore } from "../../stores/ui";
import styles from "./shell.module.css";
import { LeftRail } from "./LeftRail";
import { TopBar } from "./TopBar";

/** §2.1 / §10: the rail auto-collapses below 1100px. */
const NARROW_QUERY = "(max-width: 1099px)";

function useIsNarrow(): boolean {
  const [narrow, setNarrow] = useState(
    () => typeof window.matchMedia === "function" && window.matchMedia(NARROW_QUERY).matches,
  );
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia(NARROW_QUERY);
    const onChange = () => setNarrow(mq.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);
  return narrow;
}

/**
 * The shell's contribution to the keyboard map (UI spec §6): rail collapse, the two
 * overlays, and the G-prefixed navigation chords. Project chords are enabled only while a
 * project is open. Registered here — the cheatsheet and palette read them from the registry.
 */
function useShellKeyBindings(projectKey: string | undefined): void {
  const navigate = useNavigate();
  useKeyBindings(() => {
    const ui = useUIStore.getState;
    const inProject = () => projectKey !== undefined;
    const go = (to: string): (() => void) => {
      return () => {
        const href = projectKey ? to.replace("$key", projectKey) : to;
        void navigate({ to: href });
      };
    };
    const nav = (
      id: string,
      chord: string,
      title: string,
      to: string,
      projectScoped = false,
    ): KeyBinding => ({
      id,
      scope: "global",
      chord,
      title,
      group: "Navigation",
      palette: true,
      enabled: projectScoped ? inProject : undefined,
      run: go(to),
    });
    return [
      {
        id: "shell.rail",
        scope: "global",
        chord: "mod+\\",
        title: "Collapse left rail",
        group: "General",
        palette: true,
        run: () => ui().toggleRail(),
      },
      {
        id: "shell.cheatsheet",
        scope: "global",
        chord: "?",
        title: "Shortcut cheatsheet",
        group: "General",
        run: () => ui().setCheatsheetOpen(!ui().cheatsheetOpen),
      },
      {
        id: "shell.palette",
        scope: "global",
        chord: "mod+k",
        title: "Command palette",
        group: "General",
        run: () => ui().setPaletteOpen(!ui().paletteOpen),
      },
      nav("go.home", "g h", "Go to Home", "/"),
      nav("go.inbox", "g i", "Go to Inbox", "/inbox"),
      nav("go.board", "g b", "Go to Board", "/p/$key/board", true),
      nav("go.wiki", "g w", "Go to Wiki", "/p/$key/wiki", true),
      nav("go.runs", "g r", "Go to Runs", "/p/$key/runs", true),
      nav("go.agents", "g a", "Go to Agents", "/p/$key/agents", true),
      nav("go.triage", "g t", "Go to Triage", "/p/$key/triage", true),
      nav("go.triggers", "g g", "Go to Triggers", "/p/$key/triggers", true),
      nav("go.settings", "g s", "Go to Project settings", "/p/$key/settings", true),
      // Same chord, the other context: outside a project "settings" can only mean the
      // workspace's own screen (§1). Exactly one of the two is ever enabled, so the registry
      // never has to choose. This also puts "Workspace settings" in the ⌘K palette, which is
      // the other place a person looks for a screen they cannot find.
      {
        id: "go.workspace-settings",
        scope: "global",
        chord: "g s",
        title: "Go to Workspace settings",
        group: "Navigation",
        palette: true,
        enabled: () => projectKey === undefined,
        run: () => void navigate({ to: "/settings" }),
      },
    ];
  }, [navigate, projectKey]);
}

export function AppShell() {
  const params = useParams({ strict: false }) as { key?: string };
  const isNarrow = useIsNarrow();
  const railCollapsed = useUIStore((s) => s.railCollapsed);
  useShellKeyBindings(params.key);

  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: ({ signal }) => authApi.me(signal),
    staleTime: 5 * 60_000,
  });

  if (me.isError) {
    const err = me.error;
    if (err instanceof ApiProblem && err.status === 401) {
      return <Navigate to={err.type === "setup_required" ? "/setup" : "/login"} replace />;
    }
    // Non-auth failure (server down, 5xx after retries): render a plain message rather than
    // a redirect loop.
    return (
      <div className={styles.bootError} role="alert">
        Lexicode could not load: {err instanceof Error ? err.message : "unknown error"}
      </div>
    );
  }
  if (me.isPending) {
    return <div className={styles.boot} aria-busy="true" />;
  }

  // Persisted preference OR the responsive breakpoint collapses the rail; widening the
  // window back restores the user's persisted choice (§2.1).
  const collapsed = railCollapsed || isNarrow;

  return (
    <div className={styles.frame} data-rail-collapsed={collapsed || undefined}>
      <TopBar user={me.data} />
      <div className={styles.body}>
        <LeftRail collapsed={collapsed} />
        <main className={styles.content}>
          <Outlet />
        </main>
      </div>
      <CommandPalette />
      <AskAgentPalette projectKey={params.key} />
      <KeyboardCheatsheet />
    </div>
  );
}

/*
 * The 40px fixed top bar (UI spec §2.1): wordmark, Home, Inbox (badge shows only an
 * actionable count), the ⌘K search affordance, the cheatsheet button and the user menu
 * (theme, density, sign out).
 */
import { useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { TabBadge } from "../../components/TabBadge/TabBadge";
import { authApi, type User } from "../../lib/api/client";
import { queryClient } from "../../lib/api/queryClient";
import { chordLabel } from "../../lib/keyboard/hooks";
import { useUIStore, type Density, type ThemePreference } from "../../stores/ui";
import styles from "./shell.module.css";

export function TopBar({ user }: { user: User }) {
  const setPaletteOpen = useUIStore((s) => s.setPaletteOpen);
  const setCheatsheetOpen = useUIStore((s) => s.setCheatsheetOpen);

  return (
    <header className={styles.topbar}>
      <Link to="/" className={styles.wordmark}>
        ◈ Lexicode
      </Link>
      <nav className={styles.topnav} aria-label="Global">
        <Link to="/" className={styles.topnavLink} activeProps={{ "data-active": "" }}>
          Home
        </Link>
        <Link to="/inbox" className={styles.topnavLink} activeProps={{ "data-active": "" }}>
          Inbox
          {/* Wired to the actionable needs-you count when the inbox API lands (S28). */}
          <TabBadge count={undefined} />
        </Link>
      </nav>
      <button
        type="button"
        className={styles.search}
        onClick={() => setPaletteOpen(true)}
        aria-label="Open command palette"
      >
        <span>{chordLabel("mod+k")}</span> search…
      </button>
      <div className={styles.topbarRight}>
        <button
          type="button"
          className={styles.iconButton}
          onClick={() => setCheatsheetOpen(true)}
          aria-label="Keyboard shortcuts"
        >
          ?
        </button>
        <UserMenu user={user} />
      </div>
    </header>
  );
}

function UserMenu({ user }: { user: User }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();
  const { theme, density, setTheme, setDensity } = useUIStore();

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener("pointerdown", onPointerDown);
    return () => window.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  const logout = useMutation({
    mutationFn: authApi.logout,
    onSuccess: () => {
      queryClient.clear();
      void navigate({ to: "/login" });
    },
  });

  return (
    <div ref={rootRef} className={styles.userMenu}>
      <button
        type="button"
        className={styles.avatar}
        style={{ background: user.avatar_color }}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {initials(user.display_name)}
      </button>
      {open && (
        <div role="menu" className={styles.menu}>
          <div className={styles.menuHeader}>
            <div>{user.display_name}</div>
            <div className={styles.menuMeta}>{user.email}</div>
          </div>
          <label className={styles.menuRow}>
            Theme
            <select
              value={theme}
              onChange={(e) => setTheme(e.target.value as ThemePreference)}
            >
              <option value="system">System</option>
              <option value="dark">Dark</option>
              <option value="light">Light</option>
            </select>
          </label>
          <label className={styles.menuRow}>
            Density
            <select
              value={density}
              onChange={(e) => setDensity(e.target.value as Density)}
            >
              <option value="comfortable">Comfortable (32px)</option>
              <option value="compact">Compact (28px)</option>
            </select>
          </label>
          <button
            type="button"
            role="menuitem"
            className={styles.menuAction}
            onClick={() => logout.mutate()}
            disabled={logout.isPending}
          >
            Sign out
          </button>
        </div>
      )}
    </div>
  );
}

function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]!.toUpperCase())
    .join("");
}

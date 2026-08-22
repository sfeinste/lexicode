/*
 * The 40px fixed top bar (UI spec §2.1): wordmark, Home, Inbox (badge shows only an
 * actionable count), the ⌘K search affordance, the cheatsheet button and the user menu
 * (theme, density, sign out).
 */
import { useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { TabBadge } from "../../components/TabBadge/TabBadge";
import { authApi, type Notification, type User } from "../../lib/api/client";
import {
  useMarkNotificationRead,
  useNotificationsQuery,
} from "../../lib/api/attentionQueries";
import { queryClient } from "../../lib/api/queryClient";
import { chordLabel } from "../../lib/keyboard/hooks";
import { usePushNotifications } from "../../lib/push/usePush";
import { useStreamTopics } from "../../lib/sse/useStreamTopics";
import { useUIStore, type Density, type ThemePreference } from "../../stores/ui";
import styles from "./shell.module.css";

export function TopBar({ user }: { user: User }) {
  const setPaletteOpen = useUIStore((s) => s.setPaletteOpen);
  const setCheatsheetOpen = useUIStore((s) => s.setCheatsheetOpen);

  // The inbox topic keeps the badge live: escalation upserts arrive as
  // notification.updated frames and invalidate ["notifications"] (S24).
  useStreamTopics(["inbox"]);
  const notifications = useNotificationsQuery();
  const unread = notifications.data?.unread ?? 0;
  // S36 push tiers: question/approval/failure push (permission requested at the first
  // occurrence, never on load); review — completed runs included — only moves the badge.
  usePushNotifications(notifications.data?.notifications);

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
          <TabBadge count={unread > 0 ? unread : undefined} />
        </Link>
        <NotificationsMenu items={notifications.data?.notifications ?? []} unread={unread} />
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

/**
 * The minimal S24 notification affordance: a bell with the unread count and a dropdown of
 * the rows — one per run, updated in place (interaction rule 3). Clicking a row marks it
 * read and jumps to the run; the full inbox page is S36.
 */
function NotificationsMenu({ items, unread }: { items: Notification[]; unread: number }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();
  const markRead = useMarkNotificationRead();

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener("pointerdown", onPointerDown);
    return () => window.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  return (
    <div ref={rootRef} className={styles.notifyMenu}>
      <button
        type="button"
        className={styles.iconButton}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={unread > 0 ? `Notifications (${unread} unread)` : "Notifications"}
        data-unread={unread > 0 || undefined}
        onClick={() => setOpen((o) => !o)}
      >
        ◔
      </button>
      {open && (
        <div role="menu" className={styles.menu} aria-label="Notifications">
          {items.length === 0 && (
            <div className={styles.menuMeta} style={{ padding: "8px 12px" }}>
              Nothing is waiting on you.
            </div>
          )}
          {items.map((n) => (
            <button
              key={n.id}
              type="button"
              role="menuitem"
              className={styles.notifyRow}
              data-state={n.state}
              onClick={() => {
                setOpen(false);
                markRead.mutate(n.id);
                if (n.run_id !== null && n.project_key !== undefined && n.project_key !== "") {
                  void navigate({
                    to: "/p/$key/runs/$id",
                    params: { key: n.project_key, id: n.run_id },
                  });
                }
              }}
            >
              <span className={styles.notifyTitle}>{n.title}</span>
              <span className={styles.notifyBody}>{n.body}</span>
            </button>
          ))}
        </div>
      )}
    </div>
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

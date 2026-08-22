/*
 * Browser push notifications (S36; architecture §12). Mounted once, in the top bar, over
 * the same notification list the badge renders — the SSE reducer invalidates
 * ["notifications"] on notification.updated, so a new push-tier row reaches this hook
 * within one refetch.
 *
 * Permission is requested at the FIRST OCCURRENCE of a push-tier notification — never on
 * page load. The first data after mount only baselines the id → updated_at snapshot; only
 * a row that appears or changes after that triggers `Notification.requestPermission()`,
 * and only when permission is still "default". A denied permission is never re-asked.
 *
 * Each push is tagged with the notification's id, so the OS notification is REPLACED in
 * place when the row updates — interaction rule 3 carried through to the desktop.
 */
import { useEffect, useRef } from "react";

import type { Notification as NotificationRow } from "../api/client";
import { freshPushes } from "./tier";

export function usePushNotifications(items: readonly NotificationRow[] | undefined): void {
  const seen = useRef<Map<string, string> | null>(null);

  useEffect(() => {
    if (items === undefined) return;
    const fresh = freshPushes(seen.current, items);
    if (seen.current === null) seen.current = new Map();
    for (const n of items) seen.current.set(n.id, n.updated_at);
    if (fresh.length > 0) void deliver(fresh);
  }, [items]);
}

async function deliver(fresh: NotificationRow[]): Promise<void> {
  if (!("Notification" in window)) return;
  let permission = window.Notification.permission;
  if (permission === "default") {
    // The first occurrence: ask now, in the moment there is something to show.
    permission = await window.Notification.requestPermission();
  }
  if (permission !== "granted") return;
  for (const n of fresh) {
    try {
      new window.Notification(n.title, { body: n.body, tag: n.id });
    } catch {
      // Some platforms only allow constructing notifications from a service worker;
      // the in-app badge and inbox still carry the row.
      return;
    }
  }
}

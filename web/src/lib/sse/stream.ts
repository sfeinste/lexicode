/*
 * The single multiplexed SSE connection per tab (architecture §13, contracts §5.1):
 * GET /api/v1/stream?topics=… over native EventSource. Components declare what they need
 * through useStreamTopics(); this manager refcounts the topic set and owns exactly one
 * EventSource.
 *
 * Reconnect semantics:
 * - Transient drops are the browser's job: EventSource reconnects on its own with backoff
 *   and sends the Last-Event-ID header, and the server replays from the event log — a
 *   dropped connection never loses log lines.
 * - When the browser gives up entirely (readyState CLOSED, e.g. the server answered a
 *   non-retriable status during a restart), the manager reopens with its own exponential
 *   backoff (1s → 30s).
 * - A CHANGE IN THE TOPIC SET RECONNECTS: EventSource cannot resubscribe in place, so the
 *   manager closes the connection and opens a new one with the new ?topics=. A fresh
 *   connection carries no Last-Event-ID, so a frame published in that gap is not replayed —
 *   callers get a fresh snapshot anyway, because every mount that adds a topic also runs its
 *   query. Frames received on the old connection are unaffected.
 *
 * Every frame lands in applyStreamEvent — the one cache-application reducer.
 */
import type { QueryClient } from "@tanstack/react-query";

import { API_BASE } from "../api/client";
import { applyStreamEvent, type StreamEventType, type StreamFrame } from "./applyEvent";

/** The event names the server emits (contracts §5.1). EventSource needs a listener each. */
export const STREAM_EVENT_TYPES: readonly StreamEventType[] = [
  "run.state",
  "run.activity",
  "run.step",
  "run.usage",
  "run.elicitation",
  "ticket.updated",
  "board.updated",
  "triage.created",
  "trigger.fired",
  "notification.updated",
  "wiki.proposed",
  "provision.step",
  "module.degraded",
];

const BACKOFF_MIN_MS = 1_000;
const BACKOFF_MAX_MS = 30_000;

export class StreamManager {
  private counts = new Map<string, number>();
  private source: EventSource | null = null;
  private connectedTopics = "";
  private backoffMs = BACKOFF_MIN_MS;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private syncScheduled = false;

  private qc: QueryClient;

  constructor(qc: QueryClient) {
    this.qc = qc;
  }

  /** Declare interest in topics; returns the release. Idempotent per call site. */
  acquire(topics: string[]): () => void {
    for (const t of topics) this.counts.set(t, (this.counts.get(t) ?? 0) + 1);
    this.scheduleSync();
    let released = false;
    return () => {
      if (released) return;
      released = true;
      for (const t of topics) {
        const n = (this.counts.get(t) ?? 1) - 1;
        if (n <= 0) this.counts.delete(t);
        else this.counts.set(t, n);
      }
      this.scheduleSync();
    };
  }

  /** The current topic set, sorted for a stable query string. */
  topics(): string[] {
    return [...this.counts.keys()].sort();
  }

  /**
   * Batch topic changes within a tick: a route transition releases one screen's topics and
   * acquires the next's — that must be one reconnect, not two.
   */
  private scheduleSync(): void {
    if (this.syncScheduled) return;
    this.syncScheduled = true;
    queueMicrotask(() => {
      this.syncScheduled = false;
      this.sync();
    });
  }

  private sync(): void {
    const desired = this.topics().join(",");
    if (desired === this.connectedTopics && (this.source || desired === "")) return;
    this.close();
    if (desired === "") return;
    this.open(desired);
  }

  private open(topicsParam: string): void {
    this.connectedTopics = topicsParam;
    const source = new EventSource(`${API_BASE}/stream?topics=${encodeURIComponent(topicsParam)}`);
    this.source = source;

    source.onopen = () => {
      this.backoffMs = BACKOFF_MIN_MS;
    };
    for (const type of STREAM_EVENT_TYPES) {
      source.addEventListener(type, (ev) => this.onFrame(type, ev as MessageEvent<string>));
    }
    source.onerror = () => {
      // CONNECTING means the browser is already retrying (with Last-Event-ID); leave it.
      if (source.readyState !== EventSource.CLOSED) return;
      this.close();
      this.retryTimer = setTimeout(() => {
        this.retryTimer = null;
        this.sync();
      }, this.backoffMs);
      this.backoffMs = Math.min(this.backoffMs * 2, BACKOFF_MAX_MS);
    };
  }

  private onFrame(type: StreamEventType, ev: MessageEvent<string>): void {
    let frame: StreamFrame;
    try {
      frame = JSON.parse(ev.data) as StreamFrame;
    } catch {
      return; // a malformed frame is the server's bug; do not take the tab down
    }
    applyStreamEvent(this.qc, type, frame);
  }

  private close(): void {
    if (this.retryTimer) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    this.source?.close();
    this.source = null;
    this.connectedTopics = "";
  }
}

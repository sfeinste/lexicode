/*
 * Board queries and mutations (S11). Query keys live under ["board", projectKey] so the SSE
 * reducer's ticket.* / label.* / board.updated invalidations (applyEvent.ts) hit everything
 * the board renders — a move in one tab re-renders a second tab through that one funnel.
 *
 * Mutations are optimistic (drag must feel instant): onMutate rewrites the cached ticket
 * list, onError restores the snapshot, onSettled refetches so the server's canonical
 * ordering wins. Drag never starts a run (interaction rule 2) — the drag mutations call the
 * move/patch endpoints and nothing else. Starting a run is a separate, deliberate mutation
 * (useDelegateTicket, the `D` picker) that no drag path reaches.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  api,
  ApiProblem,
  labelsApi,
  ticketsApi,
  type CreateTicketRequest,
  type MoveTicketRequest,
  type Ticket,
  type TicketListResponse,
  type UpdateTicketRequest,
} from "../../../lib/api/client";
import { runKeys } from "../../../lib/api/runQueries";

export const boardKeys = {
  all: (projectKey: string) => ["board", projectKey] as const,
  tickets: (projectKey: string) => ["board", projectKey, "tickets"] as const,
  labels: (projectKey: string) => ["board", projectKey, "labels"] as const,
  needsYou: (projectKey: string) => ["board", projectKey, "needs-you"] as const,
};

export function useBoardTickets(projectKey: string) {
  return useQuery({
    queryKey: boardKeys.tickets(projectKey),
    queryFn: ({ signal }) => ticketsApi.list(projectKey, undefined, signal),
  });
}

export function useBoardLabels(projectKey: string) {
  return useQuery({
    queryKey: boardKeys.labels(projectKey),
    queryFn: ({ signal }) => labelsApi.list(projectKey, signal),
  });
}

// ---- the needs-you lane query -----------------------------------------------------------

/**
 * One row of the pinned lane: a run blocking on a human, in the §4.3 vocabulary. The
 * runs-needing-attention endpoint (contracts §5: GET /projects/{key}/runs?view=needs_you)
 * does not exist until S21/S22 — this is the future shape, and the query returns an empty
 * lane on the 404 the missing route answers today. The component and query land now (S11)
 * so S22 only has to light up the server side.
 */
export interface NeedsYouRun {
  id: string;
  ticket_id: string | null;
  ticket_key: string | null;
  ticket_title: string | null;
  agent: string;
  /** §4.3: which of the four flavors, stated in words by the renderer. */
  flavor: "question" | "approval" | "review" | "failure";
  status: string;
  started_at: string;
}

export function useNeedsYouRuns(projectKey: string) {
  return useQuery({
    queryKey: boardKeys.needsYou(projectKey),
    queryFn: async ({ signal }): Promise<NeedsYouRun[]> => {
      try {
        const res = await api<{ runs: NeedsYouRun[] }>(
          "GET",
          `/projects/${encodeURIComponent(projectKey)}/runs?view=needs_you`,
          { signal },
        );
        return res.runs;
      } catch (err) {
        // The endpoint is not implemented yet: an empty lane, not an error banner.
        if (err instanceof ApiProblem && err.status === 404) return [];
        throw err;
      }
    },
  });
}

// ---- optimistic mutations ---------------------------------------------------------------

interface Snapshot {
  previous: TicketListResponse | undefined;
}

function useOptimisticTickets(projectKey: string) {
  const qc = useQueryClient();
  const key = boardKeys.tickets(projectKey);
  return {
    qc,
    async begin(apply: (t: Ticket) => Ticket): Promise<Snapshot> {
      await qc.cancelQueries({ queryKey: key });
      const previous = qc.getQueryData<TicketListResponse>(key);
      if (previous) {
        qc.setQueryData<TicketListResponse>(key, {
          tickets: previous.tickets.map(apply),
        });
      }
      return { previous };
    },
    rollback(snap: Snapshot | undefined) {
      if (snap?.previous) qc.setQueryData(key, snap.previous);
    },
    settle() {
      void qc.invalidateQueries({ queryKey: key });
    },
  };
}

/** Drag within/between status columns: the S10 move endpoint with a client-computed
 * fractional position. Optimistic; rolls back on error. */
export function useMoveTicket(projectKey: string) {
  const opt = useOptimisticTickets(projectKey);
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: MoveTicketRequest }) =>
      ticketsApi.move(id, body),
    onMutate: ({ id, body }) =>
      opt.begin((t) =>
        t.id === id
          ? { ...t, column_id: body.column_id, position: body.position ?? t.position }
          : t,
      ),
    onError: (_err, _vars, snap) => opt.rollback(snap),
    onSettled: () => opt.settle(),
  });
}

/** Drag between non-status groups (and the S/P/A pickers): writes the grouped property. */
export function usePatchTicket(projectKey: string) {
  const opt = useOptimisticTickets(projectKey);
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateTicketRequest }) =>
      ticketsApi.update(id, body),
    onMutate: ({ id, body }) =>
      opt.begin((t) => (t.id === id ? applyPatch(t, body) : t)),
    onError: (_err, _vars, snap) => opt.rollback(snap),
    onSettled: () => opt.settle(),
  });
}

function applyPatch(t: Ticket, body: UpdateTicketRequest): Ticket {
  const next = { ...t };
  if (body.title !== undefined) next.title = body.title;
  if (body.priority !== undefined) next.priority = body.priority;
  if (body.assignee_id !== undefined) next.assignee_id = body.assignee_id;
  if (body.delegate_agent_id !== undefined) next.delegate_agent_id = body.delegate_agent_id;
  return next;
}

/**
 * `D` — the START action (UI spec §5.3). NOT a PATCH: the delegate endpoint records the
 * agent on the ticket AND enqueues a run through the scheduler, which is the whole reason
 * the spec lists `D` alongside the Run button and triggers as one of the three ways a run
 * begins. Clearing the delegate is not this mutation — clearing a field starts nothing.
 *
 * Optimistic on the delegate field only; the run itself is the server's answer (a run id,
 * queued), so the runs list is invalidated and the caller renders the run's real state.
 */
export function useDelegateTicket(projectKey: string) {
  const opt = useOptimisticTickets(projectKey);
  return useMutation({
    mutationFn: ({ id, agentId }: { id: string; agentId: string }) =>
      ticketsApi.delegate(id, { agent_id: agentId }),
    onMutate: ({ id, agentId }) =>
      opt.begin((t) => (t.id === id ? { ...t, delegate_agent_id: agentId } : t)),
    onError: (_err, _vars, snap) => opt.rollback(snap),
    onSettled: () => {
      opt.settle();
      void opt.qc.invalidateQueries({ queryKey: boardKeys.needsYou(projectKey) });
      void opt.qc.invalidateQueries({ queryKey: runKeys.list(projectKey) });
    },
  });
}

/** Label attach/detach (drag between label groups, the L picker). */
export function useSetTicketLabel(projectKey: string) {
  const opt = useOptimisticTickets(projectKey);
  return useMutation({
    mutationFn: async ({
      id,
      attach,
      detach,
    }: {
      id: string;
      attach?: string;
      detach?: string;
    }) => {
      if (attach !== undefined) await ticketsApi.attachLabel(id, attach);
      if (detach !== undefined) await ticketsApi.detachLabel(id, detach);
    },
    onMutate: ({ id, attach, detach }) =>
      opt.begin((t) => {
        if (t.id !== id) return t;
        let labelIDs = t.label_ids;
        if (attach !== undefined && !labelIDs.includes(attach)) labelIDs = [...labelIDs, attach];
        if (detach !== undefined) labelIDs = labelIDs.filter((l) => l !== detach);
        return { ...t, label_ids: labelIDs };
      }),
    onError: (_err, _vars, snap) => opt.rollback(snap),
    onSettled: () => opt.settle(),
  });
}

/** `C` — the minimal inline create: a title lands in the first backlog column. */
export function useCreateTicket(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateTicketRequest) => ticketsApi.create(projectKey, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: boardKeys.tickets(projectKey) });
    },
  });
}

/*
 * Ticket-detail queries and mutations (S12). Keys live under ["ticket", projectKey] so the
 * SSE reducer's ticket.* invalidations (applyEvent.ts — topic "project:KEY") refresh the
 * open detail, and under ["board", projectKey] cross-invalidation the board stays honest
 * after sidebar edits.
 *
 * Route → ticket resolution: the URL carries the ticket number (/p/PAY/t/14) but the API
 * addresses tickets by id. There is no by-key endpoint (deliberately — the list is already
 * cached for the board), so the page resolves the id from the project ticket list and then
 * loads the detail. Two cached queries, one extra round-trip on a cold open.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  criteriaApi,
  labelsApi,
  ticketsApi,
  usersApi,
  type CreateCommentRequest,
  type UpdateCriterionRequest,
  type UpdateTicketRequest,
} from "../../../lib/api/client";
import { useColumnsQuery } from "../../../lib/api/columnQueries";

export const ticketKeys = {
  all: (projectKey: string) => ["ticket", projectKey] as const,
  list: (projectKey: string) => ["ticket", projectKey, "list"] as const,
  detail: (projectKey: string, id: string) => ["ticket", projectKey, "detail", id] as const,
  stream: (projectKey: string, id: string) => ["ticket", projectKey, "stream", id] as const,
};

/** The project ticket list, cached under the ticket family for id resolution. */
export function useTicketList(projectKey: string) {
  return useQuery({
    queryKey: ticketKeys.list(projectKey),
    queryFn: ({ signal }) => ticketsApi.list(projectKey, { archived: true }, signal),
  });
}

/** Resolve /p/:key/t/:num → the ticket id via the cached list. */
export function useTicketId(projectKey: string, num: string): {
  id: string | undefined;
  isPending: boolean;
  isError: boolean;
  error: unknown;
} {
  const list = useTicketList(projectKey);
  const wanted = `${projectKey}-${num}`;
  const id = list.data?.tickets.find((t) => t.key === wanted)?.id;
  return { id, isPending: list.isPending, isError: list.isError, error: list.error };
}

export function useTicketDetail(projectKey: string, id: string | undefined) {
  return useQuery({
    queryKey: ticketKeys.detail(projectKey, id ?? ""),
    queryFn: ({ signal }) => ticketsApi.get(id as string, signal),
    enabled: id !== undefined,
  });
}

export function useTicketStream(projectKey: string, id: string | undefined) {
  return useQuery({
    queryKey: ticketKeys.stream(projectKey, id ?? ""),
    queryFn: ({ signal }) => ticketsApi.stream(id as string, signal),
    enabled: id !== undefined,
  });
}

export function useMembers() {
  return useQuery({
    queryKey: ["users", "list"],
    queryFn: ({ signal }) => usersApi.list(signal),
    staleTime: 5 * 60_000,
  });
}

export function useProjectLabels(projectKey: string) {
  return useQuery({
    queryKey: ["board", projectKey, "labels"],
    queryFn: ({ signal }) => labelsApi.list(projectKey, signal),
  });
}

export { useColumnsQuery };

function useInvalidateTicket(projectKey: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ticketKeys.all(projectKey) });
    void qc.invalidateQueries({ queryKey: ["board", projectKey] });
  };
}

export function usePatchTicket(projectKey: string) {
  const invalidate = useInvalidateTicket(projectKey);
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateTicketRequest }) =>
      ticketsApi.update(id, body),
    onSuccess: invalidate,
  });
}

export function useMoveTicket(projectKey: string) {
  const invalidate = useInvalidateTicket(projectKey);
  return useMutation({
    mutationFn: ({ id, columnId }: { id: string; columnId: string }) =>
      ticketsApi.move(id, { column_id: columnId }),
    onSuccess: invalidate,
  });
}

export function usePostComment(projectKey: string) {
  const invalidate = useInvalidateTicket(projectKey);
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: CreateCommentRequest }) =>
      ticketsApi.comment(id, body),
    onSuccess: invalidate,
  });
}

export function useAddCriterion(projectKey: string) {
  const invalidate = useInvalidateTicket(projectKey);
  return useMutation({
    mutationFn: ({ id, text }: { id: string; text: string }) =>
      ticketsApi.addCriterion(id, { text }),
    onSuccess: invalidate,
  });
}

export function useUpdateCriterion(projectKey: string) {
  const invalidate = useInvalidateTicket(projectKey);
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateCriterionRequest }) =>
      criteriaApi.update(id, body),
    onSuccess: invalidate,
  });
}

export function useDeleteCriterion(projectKey: string) {
  const invalidate = useInvalidateTicket(projectKey);
  return useMutation({
    mutationFn: (id: string) => criteriaApi.remove(id),
    onSuccess: invalidate,
  });
}

export function useCreateSubtickets(projectKey: string) {
  const invalidate = useInvalidateTicket(projectKey);
  return useMutation({
    mutationFn: ({ id, titles }: { id: string; titles: string[] }) =>
      ticketsApi.subtickets(id, { titles }),
    onSuccess: invalidate,
  });
}

export function useSetLabel(projectKey: string) {
  const invalidate = useInvalidateTicket(projectKey);
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
    onSuccess: invalidate,
  });
}

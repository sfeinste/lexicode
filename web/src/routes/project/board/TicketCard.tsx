/*
 * The board card (UI spec §5.3), one component for both layouts: `variant="card"` is the
 * board rendering, `variant="row"` the list rendering. Anatomy per the spec — key (mono),
 * needs-you corner badge only when a run needs a human, title clamped to 2 lines, criteria
 * progress only if criteria exist, then the earned bottom badges: [delegate] ⟶ @assignee,
 * PR. Badge earning is decided in badges.ts; this file only lays out what was earned.
 */
import type { Label, Ticket } from "../../../lib/api/client";
import { cardBadges } from "./badges";
import styles from "./board.module.css";

/** §4.3: the four flavors, stated in words — never a generic "waiting" (rule 1). */
export const NEEDS_YOU_FLAVORS = {
  question: "Answer a question",
  approval: "Approve a plan",
  review: "Review output",
  failure: "Fix a failure",
} as const;

export type NeedsYouFlavor = keyof typeof NEEDS_YOU_FLAVORS;

export interface DisplayProps {
  priority: boolean;
  labels: boolean;
  criteria: boolean;
  people: boolean;
  pr: boolean;
}

export interface TicketCardProps {
  ticket: Ticket;
  variant: "card" | "row";
  labelsById: Map<string, Label>;
  /** Agent id → name for the delegate badge (S16); an unknown id renders as-is. */
  agentNamesById?: Map<string, string>;
  show: DisplayProps;
  selected: boolean;
  multiSelected: boolean;
  /** Set when this ticket's run is in a needs-you state (S22 populates it). */
  needsYou?: NeedsYouFlavor;
  onClick: () => void;
  onDoubleClick: () => void;
  draggable: boolean;
  onDragStart: (e: React.DragEvent) => void;
  onDragEnd: () => void;
}

const PRIORITY_GLYPHS: Record<Ticket["priority"], string> = {
  urgent: "‼",
  high: "▮▮▮",
  medium: "▮▮▯",
  low: "▮▯▯",
  none: "",
};

export function TicketCard({
  ticket: t,
  variant,
  labelsById,
  agentNamesById,
  show,
  selected,
  multiSelected,
  needsYou,
  onClick,
  onDoubleClick,
  draggable,
  onDragStart,
  onDragEnd,
}: TicketCardProps) {
  const badges = cardBadges(t);
  const labels = show.labels
    ? t.label_ids.map((id) => labelsById.get(id)).filter((l): l is Label => l !== undefined)
    : [];

  const meta = (
    <>
      {show.criteria && badges.criteria !== null && (
        <div className={styles.cardCriteria}>▸ {badges.criteria}</div>
      )}
      {(labels.length > 0 ||
        (show.people && badges.hasPeopleRow) ||
        (show.pr && badges.pr !== null) ||
        (show.priority && t.priority !== "none")) && (
        <div className={styles.cardFooter}>
          {show.priority && t.priority !== "none" && (
            <span className={styles.cardPriority} title={`Priority: ${t.priority}`}>
              {PRIORITY_GLYPHS[t.priority]}
            </span>
          )}
          {show.people && badges.hasPeopleRow && (
            <span className={styles.cardPeople}>
              {badges.delegate !== null && (
                <span className={styles.cardDelegate}>
                  [{agentNamesById?.get(badges.delegate) ?? badges.delegate}]
                </span>
              )}
              {badges.delegate !== null && badges.assignee !== null && (
                <span aria-hidden="true"> ⟶ </span>
              )}
              {badges.assignee !== null && (
                <span className={styles.cardAssignee}>@{badges.assignee}</span>
              )}
            </span>
          )}
          {labels.map((l) => (
            <span key={l.id} className={styles.cardLabel}>
              <span className={styles.labelSwatch} style={{ background: l.color }} />
              {l.name}
            </span>
          ))}
          {show.pr && badges.pr !== null && <span className={styles.cardPr}>{badges.pr}</span>}
        </div>
      )}
    </>
  );

  return (
    <div
      role="button"
      tabIndex={0}
      data-ticket-key={t.key}
      data-selected={selected || undefined}
      data-multi={multiSelected || undefined}
      className={variant === "card" ? styles.card : styles.rowCard}
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      onKeyDown={(e) => {
        if (e.key === "Enter") onDoubleClick();
      }}
      draggable={draggable}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
    >
      <div className={styles.cardTop}>
        <span className={styles.cardKey}>{t.key}</span>
        {needsYou !== undefined && (
          // The §5.3 anatomy's corner badge is "▲ needs you"; the flavor in words (§4.3
          // rule 1) is one hover away and spelled out in full on the pinned lane's card.
          <span className={styles.cardNeedsYou} title={NEEDS_YOU_FLAVORS[needsYou]}>
            ▲ needs you
          </span>
        )}
      </div>
      <div className={variant === "card" ? styles.cardTitle : styles.rowTitle}>{t.title}</div>
      {meta}
    </div>
  );
}

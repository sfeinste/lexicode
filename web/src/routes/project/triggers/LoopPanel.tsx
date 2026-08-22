/*
 * The loop-protection panel (UI spec §5.9): always visible in the editor, defaults on, one
 * row per layer — a toggle plus the spec table's plain-language description, verbatim —
 * and the layer's knob where it has one (debounce window, depth, per-rule budget).
 *
 * There is deliberately NO "disable all" control: each layer is only ever toggled
 * individually (the S29 acceptance). Off-states map to the data-model §6.1 shape the guard
 * reads: debounce off is `debounce_seconds: 0`, depth off is `depth_limit: 0`, budget
 * "inherits" is `daily_budget_cents: null` (the project ceiling still applies — the guard
 * enforces it regardless; the toggle only adds a tighter per-rule ceiling).
 */
import type { LoopConfig } from "../../../lib/api/client";
import { DEFAULT_LOOP_CONFIG } from "./loopDefaults";
import styles from "./triggers.module.css";

export interface LoopPanelProps {
  value: LoopConfig;
  onChange: (next: LoopConfig) => void;
}

export function LoopPanel({ value, onChange }: LoopPanelProps) {
  const v = { ...DEFAULT_LOOP_CONFIG, ...value };
  const set = (patch: Partial<LoopConfig>) => onChange({ ...v, ...patch });

  const debounceOn = (v.debounce_seconds ?? 0) > 0;
  const depthOn = (v.depth_limit ?? 0) > 0;
  const budgetOwn = v.daily_budget_cents != null && v.daily_budget_cents > 0;

  return (
    <section className={styles.loopPanel} aria-label="Loop protection">
      <h2 className={styles.sectionHead}>Loop protection</h2>
      <LayerRow
        name="Actor suppression"
        description="Ignore events caused by this agent's own identity"
        checked={v.actor_suppression ?? true}
        onToggle={(on) => set({ actor_suppression: on })}
      />
      <LayerRow
        name="Debounce"
        description="Collapse a burst of pushes into one run"
        checked={debounceOn}
        onToggle={(on) => set({ debounce_seconds: on ? 90 : 0 })}
      >
        {debounceOn && (
          <label className={styles.knob}>
            window
            <input
              type="number"
              min={1}
              value={v.debounce_seconds ?? 90}
              onChange={(e) => set({ debounce_seconds: Math.max(1, Number(e.target.value) || 1) })}
              aria-label="Debounce window in seconds"
            />
            s
          </label>
        )}
      </LayerRow>
      <LayerRow
        name="Cancel in progress"
        description="A new push supersedes the running review"
        checked={v.cancel_in_progress ?? true}
        onToggle={(on) => set({ cancel_in_progress: on })}
      />
      <LayerRow
        name="Depth limit"
        description="Stop after 3 agent-caused re-triggers on the same PR"
        checked={depthOn}
        onToggle={(on) => set({ depth_limit: on ? 3 : 0 })}
      >
        {depthOn && (
          <label className={styles.knob}>
            depth
            <input
              type="number"
              min={1}
              value={v.depth_limit ?? 3}
              onChange={(e) => set({ depth_limit: Math.max(1, Number(e.target.value) || 1) })}
              aria-label="Depth limit value"
            />
          </label>
        )}
      </LayerRow>
      <LayerRow
        name="Budget ceiling"
        description="Stop when this rule has spent $X today"
        checked={budgetOwn}
        onToggle={(on) => set({ daily_budget_cents: on ? 500 : null })}
      >
        {budgetOwn ? (
          <label className={styles.knob}>
            $
            <input
              type="number"
              min={0.01}
              step={0.01}
              value={(v.daily_budget_cents ?? 0) / 100}
              onChange={(e) =>
                set({
                  daily_budget_cents: Math.max(1, Math.round((Number(e.target.value) || 0) * 100)),
                })
              }
              aria-label="Daily budget in dollars"
            />
            /day
          </label>
        ) : (
          <span className={styles.knobHint}>inherits the project ceiling</span>
        )}
      </LayerRow>
    </section>
  );
}

function LayerRow({
  name,
  description,
  checked,
  onToggle,
  children,
}: {
  name: string;
  description: string;
  checked: boolean;
  onToggle: (on: boolean) => void;
  children?: React.ReactNode;
}) {
  return (
    <div className={styles.loopRow} data-off={!checked || undefined}>
      <label className={styles.toggle}>
        <input
          type="checkbox"
          checked={checked}
          onChange={(e) => onToggle(e.target.checked)}
          aria-label={name}
        />
        <span className={styles.loopName}>{name}</span>
      </label>
      <span className={styles.loopDescription}>“{description}”</span>
      {children}
    </div>
  );
}

/*
 * The WHEN / IF / THEN editor (UI spec §5.9), generated entirely from the trigger catalog:
 *
 * - WHEN: event picker grouped by source → activity-type chips (multi-select; a chip that
 *   carries catalog Help — `opened` vs `synchronize` — is visually distinct and its help
 *   text renders below) → filters from the descriptor's FilterFields.
 * - IF: field | operator | value rows. Operators are type-prefixed in the dropdown
 *   ("(text) contains") and filtered by the selected field's type. `+ And` is an inline
 *   link; `Add Or group` is a separate, heavier button — OR should feel rarer.
 * - THEN: action picker rendering each action's Schema(): agent picker for type "agent",
 *   category/enum selects, template inputs with the {{...}} interpolation field picker,
 *   ordered with move/remove controls.
 *
 * The component is deliberately router- and query-free: it renders a draft and calls
 * onChange. That is what makes the S29 "novel catalog renders with no code change" test
 * possible — and what S32's cron source rides on.
 */
import { useMemo, useState } from "react";

import type {
  ActionParamField,
  CatalogEvent,
  CatalogOperator,
  LoopConfig,
  TriggerCatalog,
} from "../../../lib/api/client";
import type { ConditionGroups, ConditionRow } from "./conditions";
import { LoopPanel } from "./LoopPanel";
import styles from "./triggers.module.css";

// ---- draft model -------------------------------------------------------------------------

export interface DraftAction {
  action_id: string;
  params: Record<string, unknown>;
}

export interface TriggerDraft {
  name: string;
  source_id: string;
  event: string;
  activity_types: string[];
  filters: Record<string, string[]>;
  /** OR groups of AND rows; null means the stored tree was too deep for rows — raw mode. */
  groups: ConditionGroups | null;
  /** The raw conditions JSON, edited directly when groups is null. */
  rawConditions: string;
  actions: DraftAction[];
  loop_config: LoopConfig;
}

/** Server validation problems, keyed by the API's field names. */
export type FieldErrors = Record<string, string[]>;

export interface AgentOption {
  id: string;
  name: string;
}

export interface TriggerFormProps {
  catalog: TriggerCatalog;
  agents: AgentOption[];
  draft: TriggerDraft;
  onChange: (next: TriggerDraft) => void;
  errors: FieldErrors;
}

// ---- helpers -----------------------------------------------------------------------------

/** The pseudo-field the actor.* operators hang off ("author is an agent"). */
const ACTOR_FIELD = "@actor";

function findEvent(catalog: TriggerCatalog, sourceId: string, kind: string): CatalogEvent | undefined {
  return catalog.sources.find((s) => s.id === sourceId)?.events.find((e) => e.kind === kind);
}

function fieldFamily(event: CatalogEvent | undefined, field: string): string {
  if (field === ACTOR_FIELD || field === "") return "actor";
  return event?.fields?.find((f) => f.path === field)?.type ?? "text";
}

function opsForFamily(operators: CatalogOperator[], family: string): CatalogOperator[] {
  return operators.filter((o) => o.family === family);
}

function defaultValueFor(op: CatalogOperator | undefined): unknown {
  switch (op?.value) {
    case "number":
      return 0;
    case "bool":
      return true;
    case "enum_list":
      return [];
    case "none":
      return undefined;
    default:
      return "";
  }
}

function csv(values: string[] | undefined): string {
  return (values ?? []).join(", ");
}

function uncsv(text: string): string[] {
  return text
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

function Errors({ errors, fields }: { errors: FieldErrors; fields: string[] }) {
  const messages = fields.flatMap((f) => errors[f] ?? []);
  if (messages.length === 0) return null;
  return (
    <ul className={styles.fieldErrors}>
      {messages.map((m, i) => (
        <li key={i}>{m}</li>
      ))}
    </ul>
  );
}

// ---- the form ----------------------------------------------------------------------------

export function TriggerForm({ catalog, agents, draft, onChange, errors }: TriggerFormProps) {
  const set = (patch: Partial<TriggerDraft>) => onChange({ ...draft, ...patch });
  const event = findEvent(catalog, draft.source_id, draft.event);

  return (
    <div className={styles.form}>
      <label className={styles.nameField}>
        <span className={styles.fieldLabel}>Name</span>
        <input
          type="text"
          value={draft.name}
          onChange={(e) => set({ name: e.target.value })}
          placeholder="Review new PRs"
          aria-label="Rule name"
        />
      </label>
      <Errors errors={errors} fields={["name"]} />

      <WhenSection catalog={catalog} event={event} draft={draft} set={set} errors={errors} />
      <IfSection catalog={catalog} event={event} draft={draft} set={set} errors={errors} />
      <ThenSection catalog={catalog} event={event} agents={agents} draft={draft} set={set} errors={errors} />

      <LoopPanel value={draft.loop_config} onChange={(lc) => set({ loop_config: lc })} />
      <Errors errors={errors} fields={["loop_config"]} />
    </div>
  );
}

// ---- WHEN --------------------------------------------------------------------------------

function WhenSection({
  catalog,
  event,
  draft,
  set,
  errors,
}: {
  catalog: TriggerCatalog;
  event: CatalogEvent | undefined;
  draft: TriggerDraft;
  set: (patch: Partial<TriggerDraft>) => void;
  errors: FieldErrors;
}) {
  const pickEvent = (encoded: string) => {
    const [sourceId, kind] = encoded.split("|", 2);
    set({
      source_id: sourceId,
      event: kind,
      activity_types: [],
      filters: {},
    });
  };

  const toggleActivity = (value: string) => {
    const has = draft.activity_types.includes(value);
    set({
      activity_types: has
        ? draft.activity_types.filter((v) => v !== value)
        : [...draft.activity_types, value],
    });
  };

  const helpNotes = (event?.activity_types ?? []).filter(
    (at) => at.help !== undefined && at.help !== "",
  );

  return (
    <section className={styles.editorSection} aria-label="WHEN">
      <h2 className={styles.keywordHead}>WHEN</h2>
      <div className={styles.sectionBody}>
        <label className={styles.rowField}>
          <span className={styles.fieldLabel}>Event</span>
          <select
            value={draft.event === "" ? "" : `${draft.source_id}|${draft.event}`}
            onChange={(e) => pickEvent(e.target.value)}
            aria-label="Event"
          >
            <option value="" disabled>
              Pick an event…
            </option>
            {catalog.sources.map((src) => (
              <optgroup key={src.id} label={src.id}>
                {src.events.map((ev) => (
                  <option key={ev.kind} value={`${src.id}|${ev.kind}`}>
                    {ev.label}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </label>
        <Errors errors={errors} fields={["source_id", "event"]} />

        {event !== undefined && event.activity_types.length > 0 && (
          <div className={styles.chipRow} role="group" aria-label="Activity types">
            {event.activity_types.map((at) => (
              <button
                key={at.value}
                type="button"
                className={styles.chip}
                data-selected={draft.activity_types.includes(at.value) || undefined}
                data-distinct={(at.help !== undefined && at.help !== "") || undefined}
                aria-pressed={draft.activity_types.includes(at.value)}
                onClick={() => toggleActivity(at.value)}
              >
                {at.label}
              </button>
            ))}
          </div>
        )}
        {helpNotes.length > 0 && (
          <ul className={styles.helpNotes}>
            {helpNotes.map((at) => (
              <li key={at.value}>
                <strong>{at.label}</strong> — {at.help}
              </li>
            ))}
          </ul>
        )}
        <Errors errors={errors} fields={["activity_types"]} />

        {event !== undefined && (event.filters ?? []).length > 0 && (
          <div className={styles.filterRow}>
            {(event.filters ?? []).map((f) => (
              <label key={f.key} className={styles.rowField}>
                <span className={styles.fieldLabel}>{f.label}</span>
                <input
                  type="text"
                  value={csv(draft.filters[f.key])}
                  placeholder={f.kind === "glob-list" ? "main, release/*" : "bug, urgent"}
                  onChange={(e) =>
                    set({ filters: { ...draft.filters, [f.key]: uncsv(e.target.value) } })
                  }
                  aria-label={`${f.label} filter`}
                />
              </label>
            ))}
          </div>
        )}
        <Errors errors={errors} fields={["filters", "cron"]} />
      </div>
    </section>
  );
}

// ---- IF ----------------------------------------------------------------------------------

function IfSection({
  catalog,
  event,
  draft,
  set,
  errors,
}: {
  catalog: TriggerCatalog;
  event: CatalogEvent | undefined;
  draft: TriggerDraft;
  set: (patch: Partial<TriggerDraft>) => void;
  errors: FieldErrors;
}) {
  const groups = draft.groups;

  const setGroups = (next: ConditionGroups) => set({ groups: next });

  const setRow = (gi: number, ri: number, row: ConditionRow) => {
    if (groups === null) return;
    setGroups(groups.map((g, i) => (i === gi ? g.map((r, j) => (j === ri ? row : r)) : g)));
  };

  const addRow = (gi: number) => {
    if (groups === null) return;
    const first = opsForFamily(catalog.operators, "actor")[0];
    setGroups(
      groups.map((g, i) =>
        i === gi
          ? [...g, { field: "", op: first?.op ?? "", value: defaultValueFor(first) }]
          : g,
      ),
    );
  };

  const removeRow = (gi: number, ri: number) => {
    if (groups === null) return;
    const next = groups.map((g, i) => (i === gi ? g.filter((_, j) => j !== ri) : g));
    setGroups(next.filter((g, i) => g.length > 0 || i === 0 || next.length === 1));
  };

  const addGroup = () => {
    if (groups === null) return;
    setGroups([...groups, []]);
  };

  return (
    <section className={styles.editorSection} aria-label="IF">
      <h2 className={styles.keywordHead}>IF</h2>
      <div className={styles.sectionBody}>
        {groups === null ? (
          <label className={styles.rowField}>
            <span className={styles.fieldLabel}>
              Conditions (this rule nests deeper than the row editor renders — edit as JSON)
            </span>
            <textarea
              value={draft.rawConditions}
              onChange={(e) => set({ rawConditions: e.target.value })}
              rows={6}
              aria-label="Conditions JSON"
              className={styles.rawConditions}
            />
          </label>
        ) : (
          <>
            {groups.map((rows, gi) => (
              <div key={gi} className={styles.orGroup}>
                {gi > 0 && <div className={styles.orDivider}>or</div>}
                {rows.map((row, ri) => (
                  <ConditionRowEditor
                    key={ri}
                    row={row}
                    event={event}
                    operators={catalog.operators}
                    onChange={(r) => setRow(gi, ri, r)}
                    onRemove={() => removeRow(gi, ri)}
                  />
                ))}
                <button type="button" className={styles.andLink} onClick={() => addRow(gi)}>
                  + And
                </button>
              </div>
            ))}
            <button type="button" className={styles.orButton} onClick={addGroup}>
              Add Or group
            </button>
          </>
        )}
        <Errors errors={errors} fields={["conditions"]} />
      </div>
    </section>
  );
}

function ConditionRowEditor({
  row,
  event,
  operators,
  onChange,
  onRemove,
}: {
  row: ConditionRow;
  event: CatalogEvent | undefined;
  operators: CatalogOperator[];
  onChange: (row: ConditionRow) => void;
  onRemove: () => void;
}) {
  const fieldValue = row.field === "" ? ACTOR_FIELD : row.field;
  const family = fieldFamily(event, fieldValue);
  const ops = opsForFamily(operators, family);
  const op = operators.find((o) => o.op === row.op);
  const catalogField = event?.fields?.find((f) => f.path === row.field);

  const pickField = (field: string) => {
    const nextFamily = fieldFamily(event, field);
    const nextOps = opsForFamily(operators, nextFamily);
    const keepOp = nextOps.some((o) => o.op === row.op);
    const nextOp = keepOp ? op : nextOps[0];
    onChange({
      field: field === ACTOR_FIELD ? "" : field,
      op: nextOp?.op ?? "",
      value: keepOp ? row.value : defaultValueFor(nextOp),
    });
  };

  const pickOp = (opId: string) => {
    const nextOp = operators.find((o) => o.op === opId);
    const sameKind = op?.value === nextOp?.value;
    onChange({ ...row, op: opId, value: sameKind ? row.value : defaultValueFor(nextOp) });
  };

  return (
    <div className={styles.conditionRow} role="group" aria-label="Condition">
      <select value={fieldValue} onChange={(e) => pickField(e.target.value)} aria-label="Field">
        <option value={ACTOR_FIELD}>author (who caused it)</option>
        {(event?.fields ?? []).map((f) => (
          <option key={f.path} value={f.path}>
            {f.path}
          </option>
        ))}
      </select>
      <select value={row.op} onChange={(e) => pickOp(e.target.value)} aria-label="Operator">
        {ops.map((o) => (
          <option key={o.op} value={o.op}>
            ({o.family}) {o.label}
          </option>
        ))}
      </select>
      <ConditionValueInput
        op={op}
        enumValues={catalogField?.enum}
        value={row.value}
        onChange={(value) => onChange({ ...row, value })}
      />
      <button type="button" className={styles.removeButton} onClick={onRemove} aria-label="Remove condition">
        ✕
      </button>
    </div>
  );
}

function ConditionValueInput({
  op,
  enumValues,
  value,
  onChange,
}: {
  op: CatalogOperator | undefined;
  enumValues: string[] | undefined;
  value: unknown;
  onChange: (value: unknown) => void;
}) {
  switch (op?.value) {
    case "none":
      return null;
    case "number":
      return (
        <input
          type="number"
          value={typeof value === "number" ? value : 0}
          onChange={(e) => onChange(Number(e.target.value))}
          aria-label="Value"
        />
      );
    case "bool":
      return (
        <select
          value={value === false ? "false" : "true"}
          onChange={(e) => onChange(e.target.value === "true")}
          aria-label="Value"
        >
          <option value="true">true</option>
          <option value="false">false</option>
        </select>
      );
    case "enum":
      if (enumValues !== undefined && enumValues.length > 0) {
        return (
          <select
            value={typeof value === "string" ? value : ""}
            onChange={(e) => onChange(e.target.value)}
            aria-label="Value"
          >
            <option value="" disabled>
              Pick…
            </option>
            {enumValues.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        );
      }
      return (
        <input
          type="text"
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.target.value)}
          aria-label="Value"
        />
      );
    case "enum_list":
      return (
        <input
          type="text"
          value={Array.isArray(value) ? value.join(", ") : ""}
          placeholder={enumValues !== undefined ? enumValues.join(", ") : "a, b, c"}
          onChange={(e) => onChange(uncsv(e.target.value))}
          aria-label="Values (comma-separated)"
        />
      );
    default:
      return (
        <input
          type="text"
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.target.value)}
          aria-label="Value"
        />
      );
  }
}

// ---- THEN --------------------------------------------------------------------------------

function ThenSection({
  catalog,
  event,
  agents,
  draft,
  set,
  errors,
}: {
  catalog: TriggerCatalog;
  event: CatalogEvent | undefined;
  agents: AgentOption[];
  draft: TriggerDraft;
  set: (patch: Partial<TriggerDraft>) => void;
  errors: FieldErrors;
}) {
  const setAction = (i: number, action: DraftAction) =>
    set({ actions: draft.actions.map((a, j) => (j === i ? action : a)) });

  const removeAction = (i: number) => set({ actions: draft.actions.filter((_, j) => j !== i) });

  const moveAction = (i: number, delta: number) => {
    const j = i + delta;
    if (j < 0 || j >= draft.actions.length) return;
    const next = [...draft.actions];
    [next[i], next[j]] = [next[j], next[i]];
    set({ actions: next });
  };

  const addAction = (actionId: string) => {
    if (actionId === "") return;
    set({ actions: [...draft.actions, { action_id: actionId, params: {} }] });
  };

  return (
    <section className={styles.editorSection} aria-label="THEN">
      <h2 className={styles.keywordHead}>THEN</h2>
      <div className={styles.sectionBody}>
        {draft.actions.map((action, i) => {
          const meta = catalog.actions.find((a) => a.id === action.action_id);
          return (
            <div key={i} className={styles.actionBlock}>
              <div className={styles.actionHead}>
                <span className={styles.actionLabel}>
                  {i + 1}. {meta?.label ?? action.action_id}
                </span>
                <span className={styles.actionControls}>
                  <button type="button" onClick={() => moveAction(i, -1)} disabled={i === 0} aria-label={`Move action ${i + 1} up`}>
                    ↑
                  </button>
                  <button
                    type="button"
                    onClick={() => moveAction(i, 1)}
                    disabled={i === draft.actions.length - 1}
                    aria-label={`Move action ${i + 1} down`}
                  >
                    ↓
                  </button>
                  <button type="button" onClick={() => removeAction(i)} aria-label={`Remove action ${i + 1}`}>
                    ✕
                  </button>
                </span>
              </div>
              {meta === undefined ? (
                <p className={styles.muted}>
                  This action is not registered on the server; it will fire as errored.
                </p>
              ) : (
                (meta.schema.fields ?? []).map((f) => (
                  <ActionParamInput
                    key={f.key}
                    field={f}
                    event={event}
                    agents={agents}
                    value={action.params[f.key]}
                    onChange={(v) =>
                      setAction(i, { ...action, params: { ...action.params, [f.key]: v } })
                    }
                  />
                ))
              )}
            </div>
          );
        })}
        <label className={styles.rowField}>
          <span className={styles.fieldLabel}>Add action</span>
          <select value="" onChange={(e) => addAction(e.target.value)} aria-label="Add action">
            <option value="">+ Add an action…</option>
            {catalog.actions.map((a) => (
              <option key={a.id} value={a.id}>
                {a.label}
              </option>
            ))}
          </select>
        </label>
        <Errors errors={errors} fields={["actions"]} />
      </div>
    </section>
  );
}

function ActionParamInput({
  field,
  event,
  agents,
  value,
  onChange,
}: {
  field: ActionParamField;
  event: CatalogEvent | undefined;
  agents: AgentOption[];
  value: unknown;
  onChange: (value: unknown) => void;
}) {
  const label = (
    <span className={styles.fieldLabel}>
      {field.label}
      {field.required && <span aria-hidden="true"> *</span>}
    </span>
  );
  const help = field.help !== undefined && field.help !== "" && (
    <span className={styles.helpText}>{field.help}</span>
  );

  switch (field.type) {
    case "agent":
      return (
        <label className={styles.rowField}>
          {label}
          <select
            value={typeof value === "string" ? value : ""}
            onChange={(e) => onChange(e.target.value)}
            aria-label={field.label}
          >
            <option value="">Pick an agent…</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
          {help}
        </label>
      );
    case "category":
    case "enum":
      return (
        <label className={styles.rowField}>
          {label}
          <select
            value={typeof value === "string" ? value : ""}
            onChange={(e) => onChange(e.target.value)}
            aria-label={field.label}
          >
            <option value="">Pick…</option>
            {(field.enum ?? []).map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
          {help}
        </label>
      );
    case "template":
      return (
        <TemplateInput field={field} event={event} value={value} onChange={onChange} help={help} label={label} />
      );
    case "number":
      return (
        <label className={styles.rowField}>
          {label}
          <input
            type="number"
            value={typeof value === "number" ? value : 0}
            onChange={(e) => onChange(Number(e.target.value))}
            aria-label={field.label}
          />
          {help}
        </label>
      );
    case "bool":
      return (
        <label className={styles.toggle}>
          <input
            type="checkbox"
            checked={value === true}
            onChange={(e) => onChange(e.target.checked)}
            aria-label={field.label}
          />
          {field.label}
          {help}
        </label>
      );
    case "list":
      return (
        <label className={styles.rowField}>
          {label}
          <input
            type="text"
            value={Array.isArray(value) ? value.join(", ") : ""}
            placeholder="one, two"
            onChange={(e) => onChange(uncsv(e.target.value))}
            aria-label={field.label}
          />
          {help}
        </label>
      );
    default:
      return (
        <label className={styles.rowField}>
          {label}
          <input
            type="text"
            value={typeof value === "string" ? value : ""}
            onChange={(e) => onChange(e.target.value)}
            aria-label={field.label}
          />
          {help}
        </label>
      );
  }
}

/** A template param: text plus the {{...}} field picker for the selected event's paths. */
function TemplateInput({
  field,
  event,
  value,
  onChange,
  help,
  label,
}: {
  field: ActionParamField;
  event: CatalogEvent | undefined;
  value: unknown;
  onChange: (value: unknown) => void;
  help: React.ReactNode;
  label: React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const paths = useMemo(() => (event?.fields ?? []).map((f) => f.path), [event]);
  const text = typeof value === "string" ? value : "";

  return (
    <div className={styles.templateField}>
      <label className={styles.rowField}>
        {label}
        <textarea
          value={text}
          rows={2}
          onChange={(e) => onChange(e.target.value)}
          aria-label={field.label}
        />
        {help}
      </label>
      <div className={styles.interpWrap}>
        <button
          type="button"
          className={styles.interpButton}
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
        >
          {"{{…}}"} insert a field
        </button>
        {open && (
          <ul className={styles.interpMenu} role="menu" aria-label="Interpolation fields">
            {paths.length === 0 ? (
              <li className={styles.muted}>Pick an event first.</li>
            ) : (
              paths.map((p) => (
                <li key={p}>
                  <button
                    type="button"
                    role="menuitem"
                    onClick={() => {
                      onChange(text === "" ? `{{${p}}}` : `${text} {{${p}}}`);
                      setOpen(false);
                    }}
                  >
                    {`{{${p}}}`}
                  </button>
                </li>
              ))
            )}
          </ul>
        )}
      </div>
    </div>
  );
}

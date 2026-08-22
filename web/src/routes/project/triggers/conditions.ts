/*
 * The IF editor's model of the stored condition tree (contracts §4.1 / data model §6).
 *
 * The editor thinks in OR groups of AND rows: `groups[i]` is one group whose rows are
 * ANDed; groups are ORed. That covers the two shapes the editor itself produces —
 * `{all: [leaf…]}` for a single group and `{any: [{all: [leaf…]}…]}` for several — plus a
 * bare leaf. Anything deeper (the API accepts arbitrary nesting) is not representable in
 * rows: parse() returns null and the editor falls back to a raw-JSON textarea rather than
 * silently flattening someone's hand-written rule.
 */

export interface ConditionRow {
  /** The payload path; "" for actor.* operators (their field is implied). */
  field: string;
  op: string;
  /** The raw JSON value the operator compares against; undefined for valueless operators. */
  value?: unknown;
}

/** OR groups of AND rows. `[[]]` is the empty rule (always true). */
export type ConditionGroups = ConditionRow[][];

interface TreeLeaf {
  field?: string;
  op: string;
  value?: unknown;
}

function isLeaf(node: unknown): node is TreeLeaf {
  if (typeof node !== "object" || node === null || Array.isArray(node)) return false;
  const keys = Object.keys(node);
  if (!keys.every((k) => k === "field" || k === "op" || k === "value")) return false;
  return typeof (node as { op?: unknown }).op === "string";
}

function leafToRow(leaf: TreeLeaf): ConditionRow {
  return { field: leaf.field ?? "", op: leaf.op, value: leaf.value };
}

function parseGroup(node: unknown): ConditionRow[] | null {
  if (isLeaf(node)) return [leafToRow(node)];
  if (typeof node !== "object" || node === null) return null;
  const all = (node as { all?: unknown }).all;
  if (!Array.isArray(all) || Object.keys(node).length !== 1) return null;
  const rows: ConditionRow[] = [];
  for (const child of all) {
    if (!isLeaf(child)) return null;
    rows.push(leafToRow(child));
  }
  return rows;
}

/** Parse a stored tree into OR groups of AND rows; null when it nests deeper than rows. */
export function parseConditions(raw: unknown): ConditionGroups | null {
  if (raw === undefined || raw === null) return [[]];
  if (isLeaf(raw)) return [[leafToRow(raw)]];
  if (typeof raw !== "object" || Array.isArray(raw)) return null;
  const keys = Object.keys(raw);
  if (keys.length === 0) return [[]];
  if (keys.length !== 1) return null;
  if (keys[0] === "all") {
    const rows = parseGroup(raw);
    return rows === null ? null : [rows];
  }
  if (keys[0] === "any") {
    const any = (raw as { any: unknown }).any;
    if (!Array.isArray(any)) return null;
    const groups: ConditionGroups = [];
    for (const child of any) {
      const rows = parseGroup(child);
      if (rows === null) return null;
      groups.push(rows);
    }
    return groups.length === 0 ? [[]] : groups;
  }
  return null;
}

/** Serialize groups back to the canonical stored shape. */
export function serializeConditions(groups: ConditionGroups): unknown {
  const clean = groups.map((rows) =>
    rows.map((r) => {
      const leaf: TreeLeaf = { op: r.op };
      if (r.field !== "") leaf.field = r.field;
      if (r.value !== undefined) leaf.value = r.value;
      return leaf;
    }),
  );
  if (clean.length === 1) return { all: clean[0] };
  return { any: clean.map((rows) => ({ all: rows })) };
}

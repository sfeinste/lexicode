/*
 * S38 acceptance: every StatusDot usage carries an accessible label. Two layers:
 *
 * 1. By construction (asserted in StatusDot.test.tsx): the component ALWAYS renders label
 *    text — the §4 vocabulary's label by default, sr-only when hideLabel is set — so a
 *    StatusDot without a label cannot exist at runtime.
 * 2. This file: a source scan over every <StatusDot …> in the codebase, asserting each
 *    usage passes a `status` prop (which supplies the vocabulary label) and that no usage
 *    passes an empty `label` (the one way to blank the accessible name).
 *
 * The scan is grep-shaped on the JSX opening tag rather than a full AST parse — the two
 * assertions only need the tag's attribute text, and a new usage that lints and
 * type-checks always matches this shape.
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const srcDir = join(here, "..", "..");

function tsxFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...tsxFiles(p));
    else if (name.endsWith(".tsx") && !name.endsWith(".test.tsx")) out.push(p);
  }
  return out;
}

interface Usage {
  file: string;
  tag: string;
}

function statusDotUsages(): Usage[] {
  const usages: Usage[] = [];
  for (const file of tsxFiles(srcDir)) {
    const src = readFileSync(file, "utf8");
    // The opening tag, across lines, up to its closing ">".
    for (const m of src.matchAll(/<StatusDot\b[^>]*>/gs)) {
      usages.push({ file: file.slice(srcDir.length + 1), tag: m[0] });
    }
  }
  return usages;
}

describe("every <StatusDot> usage is labelled (S38, UI spec §10)", () => {
  const usages = statusDotUsages();

  it("finds the usages (the scan itself works)", () => {
    expect(usages.length).toBeGreaterThan(0);
  });

  it.each(usages.map((u) => [u.file, u.tag] as const))(
    "%s passes status (the vocabulary label) — %s",
    (_file, tag) => {
      expect(tag).toMatch(/\bstatus=/);
    },
  );

  it("no usage blanks the label", () => {
    for (const u of usages) {
      expect(u.tag, `${u.file}: ${u.tag}`).not.toMatch(/label=(""|\{""\}|\{''\})/);
    }
  });
});

/*
 * S38 acceptance, UI spec §10: "Focus rings on everything interactive, --border-strong,
 * never removed."
 *
 * Two assertions over the real stylesheets:
 * 1. reset.css keeps the global :focus-visible ring in --border-strong.
 * 2. Every `outline: none` / `outline: 0` anywhere in src CSS appears ONLY inside a rule
 *    that simultaneously sets `border-color: var(--border-strong)` — the §3.1 focused-input
 *    treatment, which replaces the ring with an equally visible indicator. An outline
 *    removed without that replacement fails here.
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const srcDir = join(here, "..");

function cssFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...cssFiles(p));
    else if (name.endsWith(".css")) out.push(p);
  }
  return out;
}

/** Split a stylesheet into rule bodies with their selectors (flat; @media contents included). */
function rules(css: string): Array<{ selector: string; body: string }> {
  const out: Array<{ selector: string; body: string }> = [];
  const noComments = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const re = /([^{}]+)\{([^{}]*)\}/g;
  for (const m of noComments.matchAll(re)) {
    out.push({ selector: m[1].trim(), body: m[2] });
  }
  return out;
}

describe("focus rings are never removed (S38, UI spec §10)", () => {
  it("reset.css keeps the global :focus-visible ring in --border-strong", () => {
    const css = readFileSync(join(srcDir, "styles", "reset.css"), "utf8");
    const rule = rules(css).find((r) => r.selector === ":focus-visible");
    expect(rule, "the :focus-visible rule is gone from reset.css").toBeDefined();
    expect(rule?.body).toContain("outline: 2px solid var(--border-strong)");
  });

  const files = cssFiles(srcDir);
  it.each(files.map((f) => [f.slice(srcDir.length + 1), f] as const))(
    "%s: outline removal only alongside the --border-strong replacement",
    (_rel, file) => {
      const css = readFileSync(file, "utf8");
      for (const r of rules(css)) {
        if (/outline:\s*(none|0)\b/.test(r.body)) {
          expect(
            r.body.includes("border-color: var(--border-strong)"),
            `"${r.selector}" removes the outline without the --border-strong border`,
          ).toBe(true);
          // And only on :focus states of form fields — never on buttons/links.
          expect(
            /:focus/.test(r.selector),
            `"${r.selector}" removes an outline outside a :focus rule`,
          ).toBe(true);
        }
      }
    },
  );
});

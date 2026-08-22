/*
 * Windowing: a 500-step fixture renders only the visible window (± overscan) into the DOM —
 * the §5.7 "500 steps at 60fps" acceptance is this property plus fixed-height rows.
 */
import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { VirtualList } from "./VirtualList";

const ITEMS = Array.from({ length: 500 }, (_, i) => `step ${i}`);

function renderList(props: Partial<Parameters<typeof VirtualList<string>>[0]> = {}) {
  return render(
    <VirtualList
      items={ITEMS}
      rowHeight={28}
      defaultHeight={280}
      overscan={5}
      itemKey={(item) => item}
      renderRow={(item) => <span>{item}</span>}
      aria-label="timeline"
      {...props}
    />,
  );
}

describe("VirtualList", () => {
  it("renders only the visible window of a 500-item list", () => {
    const { container } = renderList();
    const rows = container.querySelectorAll("[data-virtual-row]");
    // viewport 280px / 28px rows = 10 visible + 5 overscan below (+0 above at the top).
    expect(rows.length).toBeLessThanOrEqual(16);
    expect(rows.length).toBeGreaterThanOrEqual(10);
    expect(rows[0].textContent).toBe("step 0");
    // The spacer keeps the scrollbar honest: total height = 500 × 28.
    const spacer = container.querySelector("[aria-label='timeline']")!.firstElementChild!;
    expect((spacer as HTMLElement).style.height).toBe(`${500 * 28}px`);
  });

  it("scrolling moves the window; far-away rows are not in the DOM", () => {
    const { container } = renderList();
    const outer = container.querySelector("[aria-label='timeline']") as HTMLElement;
    outer.scrollTop = 250 * 28; // jump to the middle
    fireEvent.scroll(outer);
    const indices = [...container.querySelectorAll("[data-virtual-row]")].map((el) =>
      Number(el.getAttribute("data-virtual-row")),
    );
    expect(Math.min(...indices)).toBe(245); // 250 - overscan
    expect(Math.max(...indices)).toBeLessThanOrEqual(266);
    expect(container.textContent).toContain("step 250");
    expect(container.textContent).not.toContain("step 0,"); // nothing from the top remains
    expect(indices).not.toContain(0);
    expect(indices).not.toContain(499);
  });

  it("scrollToIndex jumps the window to the target row", () => {
    const { container } = renderList({ scrollToIndex: 400 });
    const indices = [...container.querySelectorAll("[data-virtual-row]")].map((el) =>
      Number(el.getAttribute("data-virtual-row")),
    );
    expect(indices).toContain(400);
  });
});

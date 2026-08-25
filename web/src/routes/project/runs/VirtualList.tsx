/*
 * A hand-rolled fixed-row-height windowed list (S23). The 500-step acceptance needs the
 * timeline to scroll at 60fps; every timeline row is a one-line row of ROW height, so plain
 * scrollTop → index arithmetic beats pulling in a virtualization library (documented
 * decision: ~80 lines, zero dependencies, and the fixed height makes it exact, not
 * heuristic). Only the visible window ± overscan exists in the DOM; everything else is one
 * spacer div of the right total height.
 */
import {
  useCallback,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";

export interface VirtualListProps<T> {
  items: T[];
  rowHeight: number;
  renderRow: (item: T, index: number) => ReactNode;
  itemKey: (item: T, index: number) => string | number;
  /** Rows rendered beyond each edge of the viewport. */
  overscan?: number;
  /** Scroll so this index is visible (centered); used by ?step= permalinks and `f`. */
  scrollToIndex?: number;
  /**
   * The scroll viewport's height. Windowing needs a bounded box, and the caller owns the
   * layout — the run detail's timeline pane sizes this from the §5.7 three-pane geometry.
   */
  height?: number | string;
  /** Viewport height fallback for environments without layout (jsdom); the real height is
   * measured from the element. */
  defaultHeight?: number;
  "aria-label"?: string;
}

export function VirtualList<T>({
  items,
  rowHeight,
  renderRow,
  itemKey,
  overscan = 10,
  scrollToIndex,
  height,
  defaultHeight = 600,
  "aria-label": ariaLabel,
}: VirtualListProps<T>) {
  const outerRef = useRef<HTMLDivElement | null>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewport, setViewport] = useState(defaultHeight);

  // Measure the viewport; re-measure on resize. jsdom reports 0 → keep the fallback.
  useLayoutEffect(() => {
    const el = outerRef.current;
    if (!el) return;
    const measure = () => {
      if (el.clientHeight > 0) setViewport(el.clientHeight);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Imperative scroll for permalinks and the `f` jump: center the target row.
  useLayoutEffect(() => {
    if (scrollToIndex === undefined) return;
    const el = outerRef.current;
    if (!el) return;
    const target = scrollToIndex * rowHeight - Math.max(0, (viewport - rowHeight) / 2);
    const top = Math.max(0, Math.min(target, items.length * rowHeight - viewport));
    el.scrollTop = top;
    setScrollTop(top);
  }, [scrollToIndex, rowHeight, viewport, items.length]);

  const onScroll = useCallback(() => {
    const el = outerRef.current;
    if (el) setScrollTop(el.scrollTop);
  }, []);

  const first = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const last = Math.min(
    items.length - 1,
    Math.ceil((scrollTop + viewport) / rowHeight) + overscan,
  );

  const rows: ReactNode[] = [];
  for (let i = first; i <= last; i++) {
    const style: CSSProperties = {
      position: "absolute",
      top: i * rowHeight,
      left: 0,
      right: 0,
      height: rowHeight,
    };
    rows.push(
      <div key={itemKey(items[i], i)} style={style} data-virtual-row={i}>
        {renderRow(items[i], i)}
      </div>,
    );
  }

  return (
    <div
      ref={outerRef}
      onScroll={onScroll}
      style={{ overflowY: "auto", position: "relative", height }}
      aria-label={ariaLabel}
    >
      <div style={{ height: items.length * rowHeight, position: "relative" }}>{rows}</div>
    </div>
  );
}

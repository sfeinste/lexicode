/*
 * S08: the autosave hook — debounced merge of queued edits, "saved" on success, "error" with
 * the message on failure, and flush-on-unmount so navigation never drops a pending edit.
 */
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useAutosave } from "./autosave";

describe("useAutosave", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("debounces and merges queued patches into one save", async () => {
    const save = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(() => useAutosave<{ a?: number; b?: number }>(save, 100));

    act(() => {
      result.current.queue({ a: 1 });
      result.current.queue({ b: 2 });
      result.current.queue({ a: 3 });
    });
    expect(save).not.toHaveBeenCalled();
    expect(result.current.status).toBe("idle");

    await act(async () => {
      vi.advanceTimersByTime(100);
      await Promise.resolve();
    });
    expect(save).toHaveBeenCalledTimes(1);
    expect(save).toHaveBeenCalledWith({ a: 3, b: 2 });
    expect(result.current.status).toBe("saved");
  });

  it("reports the error message when the save fails", async () => {
    const save = vi.fn().mockRejectedValue(new Error("Validation failed"));
    const { result } = renderHook(() => useAutosave<{ a?: number }>(save, 50));

    act(() => result.current.queue({ a: 1 }));
    await act(async () => {
      vi.advanceTimersByTime(50);
      await Promise.resolve();
    });
    expect(result.current.status).toBe("error");
    expect(result.current.error).toBe("Validation failed");
  });

  it("a fresh edit restarts the debounce window", async () => {
    const save = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(() => useAutosave<{ a?: number }>(save, 100));

    act(() => result.current.queue({ a: 1 }));
    act(() => vi.advanceTimersByTime(60));
    act(() => result.current.queue({ a: 2 }));
    act(() => vi.advanceTimersByTime(60));
    expect(save).not.toHaveBeenCalled(); // 120ms elapsed but the window restarted at 60ms

    await act(async () => {
      vi.advanceTimersByTime(40);
      await Promise.resolve();
    });
    expect(save).toHaveBeenCalledTimes(1);
    expect(save).toHaveBeenCalledWith({ a: 2 });
  });

  it("flushes the pending edit on unmount (autosave survives navigation)", () => {
    const save = vi.fn().mockResolvedValue(undefined);
    const { result, unmount } = renderHook(() => useAutosave<{ a?: number }>(save, 1000));

    act(() => result.current.queue({ a: 7 }));
    expect(save).not.toHaveBeenCalled();
    unmount();
    expect(save).toHaveBeenCalledTimes(1);
    expect(save).toHaveBeenCalledWith({ a: 7 });
  });
});

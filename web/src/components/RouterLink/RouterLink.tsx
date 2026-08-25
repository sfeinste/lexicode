/*
 * The MUI ⇄ TanStack Router link adapter.
 *
 * MUI's documented "Routing libraries" integration is a `forwardRef` component handed to a
 * MUI component's polymorphic `component` prop:
 *
 *   <Button component={RouterLink} to="/p/$key/runs" params={{ key }}>
 *
 * The shim exists because MUI's `OverridableComponent` overload resolution cannot digest
 * TanStack Router's `Link`: its props are generic over the whole route tree, so TypeScript
 * rejects `to` / `params` as unknown properties when they arrive through `component`. See
 * design/ui-library-evaluation.md §5 — this is one of the named weaknesses of the
 * recommendation, not a surprise.
 *
 * The cost is real and is deliberately contained here: `to` and `params` are plain strings
 * at this boundary, so a typo in a route path is caught by the router at runtime rather
 * than by tsc at compile time. Everywhere the app uses TanStack's <Link> directly — which
 * is still most places — full route typing is unaffected.
 */
import { Link } from "@tanstack/react-router";
import { forwardRef, type ReactNode } from "react";

export interface RouterLinkProps {
  to: string;
  params?: Record<string, string>;
  search?: Record<string, unknown>;
  className?: string;
  children?: ReactNode;
}

export const RouterLink = forwardRef<HTMLAnchorElement, RouterLinkProps>(
  function RouterLink(props, ref) {
    // The single cast the shim exists to contain.
    const LinkAny = Link as unknown as (p: RouterLinkProps & { ref?: unknown }) => ReactNode;
    return <LinkAny {...props} ref={ref} />;
  },
);

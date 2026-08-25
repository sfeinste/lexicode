/*
 * Material UI ✕ TanStack Router — the one integration seam the two libraries do not close
 * for you, adapted once here rather than at every call site.
 *
 * The problem, concretely: MUI components are polymorphic through a `component` prop, and
 * TanStack Router's `Link` is typed on `to` + a `params` REDUCER. Written the obvious way —
 * `<Button component={Link} to="/p/$key" params={{ key }}>` — TypeScript rejects the
 * `params` object against `ParamsReducerFn`, and the `component` prop drops off the
 * accepted overload. It is not a runtime problem; it is a types problem, and casting it
 * away at every call site would be the wrong trade.
 *
 * The fix is TanStack Router's own documented escape hatch, `createLink`, which takes a
 * component that renders an anchor and hands back a fully typed router link that renders
 * THAT component. So: MUI's look and behaviour, the router's types and client-side
 * navigation, no cast anywhere.
 *
 * This file is the whole of the Lexicode-specific glue between the two libraries — worth
 * knowing, because "how much glue does it need" was one of the questions the library
 * comparison had to answer.
 */
import { forwardRef } from "react";
import { createLink } from "@tanstack/react-router";
import Button, { type ButtonProps } from "@mui/material/Button";
import Link, { type LinkProps } from "@mui/material/Link";

type AnchorLinkProps = Omit<LinkProps<"a">, "href" | "component">;

const MuiAnchor = forwardRef<HTMLAnchorElement, AnchorLinkProps>(function MuiAnchor(props, ref) {
  return <Link component="a" ref={ref} {...props} />;
});

const MuiAnchorButton = forwardRef<HTMLAnchorElement, Omit<ButtonProps<"a">, "href" | "component">>(
  function MuiAnchorButton(props, ref) {
    return <Button component="a" ref={ref} {...props} />;
  },
);

/** MUI's `Link`, navigated by the router. Use anywhere a `<Link>` was used before. */
export const AppLink = createLink(MuiAnchor);

/** MUI's `Button` rendered as an anchor, navigated by the router. */
export const AppLinkButton = createLink(MuiAnchorButton);

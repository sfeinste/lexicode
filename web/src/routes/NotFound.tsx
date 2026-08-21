import { Link } from "@tanstack/react-router";

import { EmptyState } from "../components/EmptyState/EmptyState";

export function NotFound() {
  return (
    <EmptyState
      headline="Page not found"
      body="This URL does not match anything in the app."
      primary={<Link to="/">Go home</Link>}
    />
  );
}

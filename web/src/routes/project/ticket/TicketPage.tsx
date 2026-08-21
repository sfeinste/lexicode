import { useParams } from "@tanstack/react-router";

import { Placeholder } from "../../Placeholder";

export function TicketPage() {
  const { key, ticket } = useParams({ from: "/shell/p/$key/t/$ticket" });
  return (
    <Placeholder title={`${key}-${ticket}`} note="Ticket detail lands in S14." />
  );
}

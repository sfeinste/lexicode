/** Split an FTS5 snippet on its match markers (\x01…\x02) into text + <mark> runs — the
 * server never ships HTML; highlighting is assembled here. */
export function renderSnippet(s: string): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  let i = 0;
  for (const part of s.split("\x01")) {
    const end = part.indexOf("\x02");
    if (end === -1) {
      if (part !== "") out.push(part);
    } else {
      out.push(<mark key={i++}>{part.slice(0, end)}</mark>);
      const rest = part.slice(end + 1);
      if (rest !== "") out.push(rest);
    }
  }
  return out;
}

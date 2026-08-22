/** The mention token the Editor writes and the backend parses — one definition client-side.
 * Mirrors internal/service/tickets/mentions.go. */
export const mentionPattern = /@\[([^\]\n]+)\]\((user|agent|wiki|ticket):([A-Za-z0-9]+)\)/g;

import { Response } from 'express';

/**
 * Stats payload broadcast to all connected SSE clients.
 */
export interface StatsPayload {
  totalTx: number;
  totalDataPoints: number;
  lastRecordWrite: string | null; // ISO string
  activeStations: number;
}

/**
 * Active SSE client connections.
 * Each entry is the Express Response stream for one browser tab.
 */
const clients = new Set<Response>();

/**
 * Register a new SSE client and wire up cleanup on disconnect.
 * Also starts a 30-second heartbeat to keep the connection alive
 * through proxies and load balancers that close idle streams.
 */
export function addSseClient(res: Response): void {
  clients.add(res);

  // SSE comment lines (starting with ':') are ignored by the browser
  // but prevent proxies from closing the connection as "idle"
  const heartbeat = setInterval(() => {
    if (!res.writableEnded) res.write(':ping\n\n');
  }, 30_000);

  res.on('close', () => {
    clearInterval(heartbeat);
    clients.delete(res);
  });
}

/**
 * Push a stats update to every connected client.
 * Uses the named event type 'stats_update' so the client can listen selectively.
 */
export function broadcastStats(stats: StatsPayload): void {
  if (clients.size === 0) return;

  const payload = `event: stats_update\ndata: ${JSON.stringify(stats)}\n\n`;

  for (const res of clients) {
    if (!res.writableEnded) res.write(payload);
  }
}

/**
 * Number of currently connected SSE clients (useful for logging/monitoring).
 */
export function connectedClientCount(): number {
  return clients.size;
}

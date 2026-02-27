import { Router, Request, Response } from 'express';
import { GlobalStats } from '../../db/models/global-stats';
import { addSseClient } from '../../service/sse';

const router = Router();

/**
 * GET /api/events
 *
 * Server-Sent Events stream. Clients connect once and receive push
 * notifications whenever global stats change (new transactions, etc.).
 *
 * On connect, the current stats are immediately sent so the client
 * doesn't have to wait for the next batch to get a value.
 *
 * Event format:
 *   event: stats_update
 *   data: {"totalTx":9001,"totalDataPoints":297033,...}
 */
router.get('/', async (req: Request, res: Response) => {
  // Required headers for SSE
  res.setHeader('Content-Type', 'text/event-stream');
  res.setHeader('Cache-Control', 'no-cache');
  res.setHeader('Connection', 'keep-alive');
  res.setHeader('X-Accel-Buffering', 'no'); // Disable Nginx response buffering
  res.flushHeaders();

  // Register this response as a live client
  addSseClient(res);

  // Send current stats immediately so the client gets a value on connect
  try {
    const stats = await GlobalStats.findOne().lean();
    if (stats && !res.writableEnded) {
      const payload = {
        totalTx: stats.totalTx,
        totalDataPoints: stats.totalDataPoints,
        lastRecordWrite: stats.lastRecordWrite?.toISOString() ?? null,
        activeStations: stats.activeStations,
      };
      res.write(`event: stats_update\ndata: ${JSON.stringify(payload)}\n\n`);
    }
  } catch {
    // Non-fatal — client will receive the next broadcast when a batch processes
  }
});

export default router;

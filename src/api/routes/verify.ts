import { Router, Request, Response } from 'express';
import mongoose from 'mongoose';
import { config } from '../../config/env';
import { WeatherRecord } from '../../db/models/weather-record';

const router = Router();

const WOC_BASE: Record<string, string> = {
  main: 'https://api.whatsonchain.com/v1/bsv/main',
  test: 'https://api.whatsonchain.com/v1/bsv/test',
};

const TXID_RE = /^[a-fA-F0-9]{64}$/;

interface VerifyResult {
  confirmed: boolean;
  blockHeight: number | null;
}

/**
 * POST /api/verify
 * Body: { txids: string[] }
 *
 * Batch-verifies one or more transaction IDs in a single request:
 *
 * 1. Fans out all WhatsOnChain queries concurrently (Promise.allSettled).
 * 2. For every confirmed txid, writes blockHeight to all WeatherRecords that
 *    share that txid inside a single MongoDB transaction so the batch is
 *    atomic — a partial DB failure rolls back all confirmed writes together.
 * 3. Returns a Record<txid, { confirmed, blockHeight }> so callers can look
 *    up any result in O(1) without iterating an array.
 *
 * Single-txid callers pass an array of length 1 — no separate endpoint needed.
 */
router.post('/', async (req: Request, res: Response) => {
  const { txids } = req.body as { txids?: unknown };

  if (!Array.isArray(txids) || txids.length === 0) {
    res.status(400).json({ error: 'Body must contain a non-empty txids array' });
    return;
  }

  if (txids.some((id) => typeof id !== 'string' || !TXID_RE.test(id))) {
    res.status(400).json({ error: 'All txids must be 64-character hex strings' });
    return;
  }

  try {
    const base = WOC_BASE[config.BSV_NETWORK] ?? WOC_BASE['test'];

    // ── 1. Fan out all WoC queries concurrently ───────────────────────────────
    const wocResults = await Promise.allSettled(
      txids.map(async (txid) => {
        const safeTxid = encodeURIComponent(txid);
        const wocRes = await fetch(`${base}/tx/${safeTxid}`);
        if (wocRes.status === 404) return { txid, blockHeight: null };
        if (!wocRes.ok) throw new Error(`WoC ${wocRes.status} for ${txid}`);
        const data = await wocRes.json() as { blockheight?: number };
        const blockHeight =
          data.blockheight && data.blockheight > 0 ? data.blockheight : null;
        return { txid, blockHeight };
      })
    );

    // Separate confirmed from unconfirmed/failed
    const results: Record<string, VerifyResult> = {};
    const confirmed: Array<{ txid: string; blockHeight: number }> = [];

    for (let i = 0; i < txids.length; i++) {
      const txid = txids[i];
      const outcome = wocResults[i];

      if (outcome.status === 'fulfilled' && outcome.value.blockHeight !== null) {
        const blockHeight = outcome.value.blockHeight;
        results[txid] = { confirmed: true, blockHeight };
        confirmed.push({ txid, blockHeight });
      } else {
        // Rejected (WoC error) or not yet mined — treat as unconfirmed
        results[txid] = { confirmed: false, blockHeight: null };
      }
    }

    // ── 2. Persist confirmed blockHeights in one transaction ─────────────────
    if (confirmed.length > 0) {
      const session = await mongoose.startSession();
      try {
        await session.withTransaction(async () => {
          // Sequential within the transaction — MongoDB sessions don't allow
          // true parallel operations on the same session handle.
          for (const { txid, blockHeight } of confirmed) {
            await WeatherRecord.updateMany(
              { txid },
              { $set: { blockHeight } },
              { session }
            );
          }
        });
      } finally {
        await session.endSession();
      }
    }

    res.json(results);
  } catch (error) {
    console.error('Error in batch verify:', error);
    res.status(500).json({ error: 'Failed to verify transactions' });
  }
});

export default router;

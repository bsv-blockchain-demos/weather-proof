import { CreateActionOutput } from '@bsv/sdk';
import { getWallet, queueFundingAction } from './wallet';
import { createHashPuzzle } from '../scripts/hash-puzzle';
import { config } from '../config/env';

/**
 * In-memory cache of available funding outputs.
 *
 * Initialized by the first getFundingOutputCount() call (which happens
 * at startup via ensureFundingOutputs). After that:
 *   - decremented by 1 each time the processor uses an output
 *   - incremented by N each time createFundingOutputs() succeeds
 *   - reset to the real count whenever getFundingOutputCount() is called
 *     (which the monitor does every MONITOR_INTERVAL seconds)
 *
 * null = not yet initialized; callers treat null as "unknown, don't skip".
 */
let cachedFundingCount: number | null = null;

/**
 * Read the cached count without hitting the wallet API.
 * Returns null if the cache has not been initialized yet.
 */
export function getCachedFundingCount(): number | null {
  return cachedFundingCount;
}

/**
 * Decrement the cache by 1 after a funding output is successfully consumed.
 * Floors at 0 to guard against any counter drift.
 */
export function decrementCachedFundingCount(): void {
  if (cachedFundingCount !== null) {
    cachedFundingCount = Math.max(0, cachedFundingCount - 1);
  }
}

/**
 * Get the real count of available funding outputs from the wallet
 * and update the in-memory cache as a side effect.
 *
 * Called at startup (via ensureFundingOutputs) and by the monitor loop
 * every MONITOR_INTERVAL seconds, so the cache is regularly corrected
 * even if the decrement logic ever drifts.
 */
export async function getFundingOutputCount(): Promise<number> {
  const wallet = await getWallet();

  const outputs = await wallet.listOutputs({
    basket: 'funding',
  });

  cachedFundingCount = outputs.totalOutputs;
  return outputs.totalOutputs;
}

/**
 * Create funding outputs in the wallet.
 * Increments the in-memory cache on success so callers see
 * the updated count without waiting for the next real check.
 */
export async function createFundingOutputs(count: number = config.FUNDING_BATCH_SIZE): Promise<string> {
  const wallet = await getWallet();

  console.log(`Creating ${count} funding outputs...`);

  const outputs: CreateActionOutput[] = [];
  for (let i = 0; i < count; i++) {
    const { lockingScript, preimage } = createHashPuzzle();

    outputs.push({
      satoshis: config.FUNDING_OUTPUT_AMOUNT,
      lockingScript,
      basket: 'funding',
      outputDescription: 'funding output',
      customInstructions: preimage,
    });
  }

  try {
    const result = await queueFundingAction(() => wallet.createAction({
      description: 'Create funding outputs',
      outputs,
      options: {
        acceptDelayedBroadcast: false,
      },
    }));

    const txid = result.txid ?? '';
    console.log(`Successfully created ${count} funding outputs in transaction: ${txid}`);

    // Keep the cache in sync — no need to hit the wallet again to know the new count
    cachedFundingCount = (cachedFundingCount ?? 0) + count;

    return txid;
  } catch (error) {
    if (error instanceof Error && error.message.includes('insufficient')) {
      console.error('Insufficient funds to create funding outputs');
      throw new Error('INSUFFICIENT_FUNDS: Please add more satoshis to the wallet');
    }
    throw error;
  }
}

/**
 * Ensure a minimum number of funding outputs are available.
 * Creates more if needed. Called at startup — this is also the point
 * where the in-memory cache is first populated.
 */
export async function ensureFundingOutputs(minCount: number = config.FUNDING_BASKET_MIN): Promise<void> {
  const currentCount = await getFundingOutputCount(); // initializes cache

  console.log(`Current funding outputs: ${currentCount}`);

  if (currentCount < minCount) {
    const needed = config.FUNDING_BATCH_SIZE;
    console.log(`Below minimum (${minCount}), creating ${needed} more...`);
    await createFundingOutputs(needed);
  } else {
    console.log(`Funding outputs sufficient (${currentCount} >= ${minCount})`);
  }
}

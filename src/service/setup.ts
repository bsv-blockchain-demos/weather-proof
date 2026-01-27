import { getWallet } from './wallet';
import { createHashPuzzle } from '../scripts/hash-puzzle';
import { config } from '../config/env';

/**
 * Create funding outputs in the wallet
 * Each output uses a hash puzzle locking script with the preimage stored in customInstructions
 *
 * @param {number} count - Number of funding outputs to create (default: FUNDING_BATCH_SIZE)
 * @returns {Promise<string>} The transaction ID of the funding transaction
 */
export async function createFundingOutputs(count: number = config.FUNDING_BATCH_SIZE): Promise<string> {
  const wallet = await getWallet();

  console.log(`Creating ${count} funding outputs...`);

  const outputs = [];
  for (let i = 0; i < count; i++) {
    const { lockingScript, preimage } = createHashPuzzle();

    outputs.push({
      satoshis: config.FUNDING_OUTPUT_AMOUNT,
      lockingScript,
      basket: 'funding',
      outputDescription: 'funding output',
      customInstructions: preimage, // Store preimage for later unlocking
    });
  }

  try {
    const result = await wallet.createAction({
      description: 'Create funding outputs',
      outputs,
    });

    const txid = result.txid ?? '';
    console.log(`Successfully created ${count} funding outputs in transaction: ${txid}`);
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
 * Get the current count of available funding outputs
 *
 * @returns {Promise<number>} The number of available funding outputs
 */
export async function getFundingOutputCount(): Promise<number> {
  const wallet = await getWallet();

  const { outputs } = await wallet.listOutputs({
    basket: 'funding',
  });

  return outputs.length;
}

/**
 * Ensure a minimum number of funding outputs are available
 * Creates more if needed
 *
 * @param {number} minCount - Minimum required outputs (default: FUNDING_BASKET_MIN)
 * @returns {Promise<void>}
 */
export async function ensureFundingOutputs(minCount: number = config.FUNDING_BASKET_MIN): Promise<void> {
  const currentCount = await getFundingOutputCount();

  console.log(`Current funding outputs: ${currentCount}`);

  if (currentCount < minCount) {
    const needed = config.FUNDING_BATCH_SIZE;
    console.log(`Below minimum (${minCount}), creating ${needed} more...`);
    await createFundingOutputs(needed);
  } else {
    console.log(`Funding outputs sufficient (${currentCount} >= ${minCount})`);
  }
}

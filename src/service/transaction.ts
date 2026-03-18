import { getWallet, queueWeatherTx } from './wallet';
import { WeatherDataEncoder } from '../format/encoder';
import { createUnlockingScript } from '../scripts/hash-puzzle';
import { IWeatherRecord } from '../db/models/weather-record';
import { config } from '../config/env';

/** Max retries when a funding UTXO turns out to be double-spent. */
const MAX_DOUBLE_SPEND_RETRIES = 5;

/**
 * Transaction result containing txid and output indexes
 */
export interface TransactionResult {
  txid: string;
  outputIndexes: number[];
}

/**
 * Check whether an error is a WERR_REVIEW_ACTIONS with a doubleSpend status.
 * This happens when a funding UTXO was already spent (e.g. by another pod
 * during a rolling deploy). The wallet marks the failed input as spent
 * internally, so the next listOutputs call will return a different UTXO.
 */
function isDoubleSpendError(error: unknown): boolean {
  if (!(error instanceof Error) || error.name !== 'WERR_REVIEW_ACTIONS') {
    return false;
  }
  const reviewResults = (error as unknown as Record<string, unknown>).reviewActionResults;
  if (!Array.isArray(reviewResults)) return false;
  return reviewResults.some(
    (r: Record<string, unknown>) => r.status === 'doubleSpend'
  );
}

/**
 * Create a transaction with weather data outputs
 * Uses one funding input to fund multiple weather outputs
 *
 * @param {IWeatherRecord[]} records - The weather records to include
 * @returns {Promise<TransactionResult>} The transaction result
 */
export async function createWeatherTransaction(records: IWeatherRecord[]): Promise<TransactionResult> {
  if (records.length === 0) {
    throw new Error('No records provided for transaction');
  }

  if (records.length > config.WEATHER_OUTPUTS_PER_TX) {
    throw new Error(`Too many records: ${records.length} (max: ${config.WEATHER_OUTPUTS_PER_TX})`);
  }

  const wallet = await getWallet();
  const encoder = new WeatherDataEncoder();

  // Create weather data outputs
  const weatherOutputs = records.map((record) => {
    const script = encoder.encode(record.data);
    return {
      satoshis: 0,
      lockingScript: script.toHex(),
      outputDescription: 'weather',
    };
  });

  try {
    // listOutputs + createAction must be inside the same queue slot.
    // Retry within the queue when a funding UTXO is double-spent —
    // each failed attempt marks that UTXO as spent internally, so the
    // next listOutputs returns a fresh one.
    const result = await queueWeatherTx(async () => {
      for (let attempt = 1; attempt <= MAX_DOUBLE_SPEND_RETRIES; attempt++) {
        const { outputs: fundingOutputs, BEEF } = await wallet.listOutputs({
          basket: 'funding',
          include: 'entire transactions',
          limit: 1,
          includeCustomInstructions: true,
        });

        if (fundingOutputs.length === 0) {
          throw new Error('No funding outputs available');
        }

        const fundingOutput = fundingOutputs[0];
        const preimage = fundingOutput.customInstructions;

        if (!preimage) {
          throw new Error('Funding output missing preimage in customInstructions');
        }

        try {
          return await wallet.createAction({
            description: `Weather data storage (${records.length} outputs)`,
            inputBEEF: BEEF,
            inputs: [
              {
                outpoint: fundingOutput.outpoint,
                unlockingScript: createUnlockingScript(preimage),
                inputDescription: 'funding input',
              },
            ],
            outputs: weatherOutputs,
            options: {
              acceptDelayedBroadcast: false,
            },
          });
        } catch (error) {
          if (isDoubleSpendError(error)) {
            console.warn(
              `Funding UTXO ${fundingOutput.outpoint} was double-spent ` +
              `(attempt ${attempt}/${MAX_DOUBLE_SPEND_RETRIES}), trying next UTXO...`
            );
            if (attempt === MAX_DOUBLE_SPEND_RETRIES) {
              throw new Error(
                `All ${MAX_DOUBLE_SPEND_RETRIES} funding UTXOs were double-spent. ` +
                `This usually happens after a deploy with overlapping instances. ` +
                `The wallet will self-heal as stale UTXOs are exhausted.`
              );
            }
            continue;
          }
          throw error;
        }
      }
      // Unreachable, but TypeScript needs it
      throw new Error('Unexpected: retry loop exited without returning or throwing');
    });

    // Output indexes are sequential starting from 0
    const outputIndexes = weatherOutputs.map((_, i) => i);

    return {
      txid: result.txid ?? '',
      outputIndexes,
    };
  } catch (error) {
    console.error('Failed to create weather transaction:', error);
    throw error;
  }
}

/**
 * Create multiple transactions in batches
 * Useful for processing large numbers of records
 *
 * @param {IWeatherRecord[]} records - All records to process
 * @param {number} batchSize - Records per transaction (default: WEATHER_OUTPUTS_PER_TX)
 * @returns {Promise<TransactionResult[]>} Array of transaction results
 */
export async function createWeatherTransactionBatch(
  records: IWeatherRecord[],
  batchSize: number = config.WEATHER_OUTPUTS_PER_TX
): Promise<TransactionResult[]> {
  const results: TransactionResult[] = [];

  for (let i = 0; i < records.length; i += batchSize) {
    const batch = records.slice(i, i + batchSize);
    const result = await createWeatherTransaction(batch);
    results.push(result);
  }

  return results;
}

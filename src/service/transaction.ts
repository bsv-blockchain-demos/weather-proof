import { getWallet } from './wallet';
import { WeatherDataEncoder } from '../format/encoder';
import { createUnlockingScript } from '../scripts/hash-puzzle';
import { IWeatherRecord } from '../db/models/weather-record';
import { config } from '../config/env';

/**
 * Transaction result containing txid and output indexes
 */
export interface TransactionResult {
  txid: string;
  outputIndexes: number[];
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

  // Get one funding input
  const { outputs: fundingOutputs, BEEF } = await wallet.listOutputs({
    basket: 'funding',
    include: 'entire transactions',
    limit: 1,
  });

  if (fundingOutputs.length === 0) {
    throw new Error('No funding outputs available');
  }

  const fundingOutput = fundingOutputs[0];
  const preimage = fundingOutput.customInstructions;

  if (!preimage) {
    throw new Error('Funding output missing preimage in customInstructions');
  }

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
    // Create transaction
    const result = await wallet.createAction({
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

import { WeatherRecord } from '../db/models/weather-record';
import { createWeatherTransaction } from './transaction';
import { config } from '../config/env';
import { NotificationService } from '../notification/interface';

/**
 * Process pending records from the queue
 * Batches records into transactions and updates with txid
 *
 * @param {NotificationService} notification - Notification service for alerts
 * @returns {Promise<number>} Number of records processed
 */
export async function processPendingRecords(notification: NotificationService): Promise<number> {
  try {
    // Get pending records in batches
    const records = await WeatherRecord.find({ status: 'pending' })
      .sort({ createdAt: 1 })
      .limit(config.WEATHER_OUTPUTS_PER_TX);

    if (records.length === 0) {
      return 0;
    }

    console.log(`Processing ${records.length} records...`);

    // Mark as processing
    const ids = records.map((r) => r._id);
    await WeatherRecord.updateMany({ _id: { $in: ids } }, { status: 'processing' });

    try {
      // Create transaction with weather outputs
      const { txid, outputIndexes } = await createWeatherTransaction(records);

      // Update records with txid and output index
      for (let i = 0; i < records.length; i++) {
        await WeatherRecord.updateOne(
          { _id: records[i]._id },
          {
            status: 'completed',
            txid,
            outputIndex: outputIndexes[i],
            processedAt: new Date(),
          }
        );
      }

      console.log(`Successfully processed ${records.length} records in tx: ${txid}`);
      return records.length;
    } catch (error) {
      // Mark as failed (will be retried as pending)
      const errorMsg = error instanceof Error ? error.message : 'Unknown error';

      await WeatherRecord.updateMany(
        { _id: { $in: ids } },
        {
          status: 'pending',
          error: errorMsg,
        }
      );

      console.error('Failed to process records:', errorMsg);

      // Only send notification for certain errors
      if (errorMsg.includes('No funding outputs available')) {
        await notification.sendError('CRITICAL: No funding outputs available for processing');
      } else if (errorMsg.includes('insufficient')) {
        await notification.sendError('CRITICAL: Insufficient funds in wallet');
      }

      throw error;
    }
  } catch (error) {
    console.error('Processor error:', error);
    throw error;
  }
}

/**
 * Start the processor loop
 * Processes pending records at configured interval
 *
 * @param {NotificationService} notification - Notification service for alerts
 * @returns {NodeJS.Timeout} The interval timer
 */
export function startProcessorLoop(notification: NotificationService): NodeJS.Timeout {
  console.log(`Starting record processor (interval: ${config.PROCESSOR_INTERVAL}s)`);

  // Run immediately on start
  processPendingRecords(notification).catch((error) => {
    console.error('Initial processing failed:', error);
  });

  // Then run at intervals
  return setInterval(async () => {
    try {
      await processPendingRecords(notification);
    } catch (error) {
      console.error('Processor loop error:', error);
    }
  }, config.PROCESSOR_INTERVAL * 1000);
}

/**
 * Stop the processor loop
 *
 * @param {NodeJS.Timeout} timer - The interval timer to stop
 */
export function stopProcessorLoop(timer: NodeJS.Timeout): void {
  clearInterval(timer);
  console.log('Stopped record processor');
}

/**
 * Process all pending records (run to completion)
 * Useful for manual processing or shutdown
 *
 * @param {NotificationService} notification - Notification service for alerts
 * @returns {Promise<number>} Total number of records processed
 */
export async function processAllPending(notification: NotificationService): Promise<number> {
  let totalProcessed = 0;
  let batchCount = 0;

  while (true) {
    const processed = await processPendingRecords(notification);

    if (processed === 0) {
      break;
    }

    totalProcessed += processed;
    batchCount++;

    console.log(`Batch ${batchCount}: processed ${processed} records (total: ${totalProcessed})`);

    // Small delay between batches to avoid overwhelming the system
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }

  console.log(`Finished processing all pending records: ${totalProcessed} total`);
  return totalProcessed;
}

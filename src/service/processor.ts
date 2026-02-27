import { WeatherRecord, IWeatherRecord } from '../db/models/weather-record';
import { Station } from '../db/models/station';
import { incrementGlobalStats } from '../db/models/global-stats';
import { broadcastStats } from './sse';
import { createWeatherTransaction } from './transaction';
import { config } from '../config/env';
import { NotificationService } from '../notification/interface';

/**
 * Process pending records from the queue.
 * Batches records into transactions and updates with txid.
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

    // Mark as processing — single bulkWrite instead of N awaits
    const ids = records.map((r) => r._id);
    await WeatherRecord.bulkWrite(
      ids.map((id) => ({
        updateOne: {
          filter: { _id: id },
          update: { $set: { status: 'processing' } },
        },
      })),
      { ordered: false }
    );

    try {
      // Create transaction with weather outputs
      const { txid, outputIndexes } = await createWeatherTransaction(records);

      const processedAt = new Date();

      // Update all records in one bulkWrite round-trip
      await WeatherRecord.bulkWrite(
        records.map((record, i) => ({
          updateOne: {
            filter: { _id: record._id },
            update: {
              $set: {
                status: 'completed',
                txid,
                outputIndex: outputIndexes[i],
                processedAt,
              },
            },
          },
        })),
        { ordered: false }
      );

      // Update denormalized Station docs grouped by stationId.
      // Records are sorted ascending by createdAt so the last entry
      // for each stationId in the loop is the most recent one.
      const byStation = buildStationGroups(records);

      const stationOps = Array.from(byStation.entries()).map(([stationId, { count, latest }]) => ({
        updateOne: {
          filter: { stationId },
          update: {
            $inc: { txRecords: count },
            $set: {
              lastReading: latest.timestamp,
              lastTemp: latest.data.air_temperature,
              lastConditions: latest.data.conditions,
              isActive: true,
            },
            // Only set name/location on first insert — don't overwrite manually configured values
            $setOnInsert: { name: '', location: '' },
          },
          upsert: true,
        },
      }));

      const stationResult = await Station.bulkWrite(stationOps, { ordered: false });

      // Increment global counters atomically.
      // upsertedCount tells us how many brand-new station IDs appeared in this batch.
      const updatedStats = await incrementGlobalStats(records.length, stationResult.upsertedCount);

      // Push the new stats to every connected dashboard tab immediately.
      broadcastStats({
        totalTx: updatedStats.totalTx,
        totalDataPoints: updatedStats.totalDataPoints,
        lastRecordWrite: updatedStats.lastRecordWrite?.toISOString() ?? null,
        activeStations: updatedStats.activeStations,
      });

      console.log(`Successfully processed ${records.length} records in tx: ${txid}`);
      return records.length;
    } catch (error) {
      // Roll back to pending so the batch can be retried
      const errorMsg = error instanceof Error ? error.message : 'Unknown error';

      await WeatherRecord.bulkWrite(
        ids.map((id) => ({
          updateOne: {
            filter: { _id: id },
            update: { $set: { status: 'pending', error: errorMsg } },
          },
        })),
        { ordered: false }
      );

      console.error('Failed to process records:', errorMsg);

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
 * Group records by stationId, tracking count and the most recent record
 * for each station (used to update denormalized Station doc fields).
 * Records arrive sorted ascending by createdAt so the last one wins.
 */
function buildStationGroups(
  records: IWeatherRecord[]
): Map<number, { count: number; latest: IWeatherRecord }> {
  const map = new Map<number, { count: number; latest: IWeatherRecord }>();

  for (const record of records) {
    const existing = map.get(record.stationId);
    map.set(record.stationId, {
      count: (existing?.count ?? 0) + 1,
      latest: record, // ascending sort → later iterations = more recent
    });
  }

  return map;
}

/**
 * Start the processor loop.
 *
 * @param {NotificationService} notification - Notification service for alerts
 * @returns {NodeJS.Timeout} The interval timer
 */
export function startProcessorLoop(notification: NotificationService): NodeJS.Timeout {
  console.log(`Starting record processor (interval: ${config.PROCESSOR_INTERVAL}s)`);

  processPendingRecords(notification).catch((error) => {
    console.error('Initial processing failed:', error);
  });

  return setInterval(async () => {
    try {
      await processPendingRecords(notification);
    } catch (error) {
      console.error('Processor loop error:', error);
    }
  }, config.PROCESSOR_INTERVAL * 1000);
}

/**
 * Stop the processor loop.
 *
 * @param {NodeJS.Timeout} timer - The interval timer to stop
 */
export function stopProcessorLoop(timer: NodeJS.Timeout): void {
  clearInterval(timer);
  console.log('Stopped record processor');
}

/**
 * Process all pending records (run to completion).
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

    await new Promise((resolve) => setTimeout(resolve, 1000));
  }

  console.log(`Finished processing all pending records: ${totalProcessed} total`);
  return totalProcessed;
}

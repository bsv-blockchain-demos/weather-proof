import { getStations, getCurrentConditions } from './tempest';
import { WeatherRecord } from '../db/models/weather-record';
import { config } from '../config/env';
import { NotificationService } from '../notification/interface';

/**
 * Poll weather data from Tempest API and store in queue
 *
 * @param {NotificationService} notification - Notification service for alerts
 * @returns {Promise<number>} Number of records created
 */
export async function pollWeatherData(notification: NotificationService): Promise<number> {
  let recordsCreated = 0;
  let errors = 0;

  try {
    const stations = await getStations();
    console.log(`Polling ${stations.length} stations...`);

    for (const stationId of stations) {
      try {
        const data = await getCurrentConditions(stationId);

        await WeatherRecord.create({
          stationId,
          timestamp: new Date(),
          data,
          status: 'pending',
          createdAt: new Date(),
        });

        recordsCreated++;
      } catch (error) {
        errors++;
        const errorMsg = error instanceof Error ? error.message : 'Unknown error';
        console.error(`Failed to fetch data for station ${stationId}:`, errorMsg);

        // Only send notification for first few errors to avoid spam
        if (errors <= 3) {
          await notification.sendError(`Failed to fetch data for station ${stationId}: ${errorMsg}`);
        }
      }
    }

    console.log(`Poll complete: ${recordsCreated} records created, ${errors} errors`);

    if (errors > 3) {
      await notification.sendWarning(`Poll completed with ${errors} errors (showing first 3 only)`);
    }

    return recordsCreated;
  } catch (error) {
    const errorMsg = error instanceof Error ? error.message : 'Unknown error';
    console.error('Failed to poll weather data:', errorMsg);
    await notification.sendError(`Failed to poll weather data: ${errorMsg}`);
    throw error;
  }
}

/**
 * Start the polling loop
 * Polls Tempest API at configured interval
 *
 * @param {NotificationService} notification - Notification service for alerts
 * @returns {NodeJS.Timeout} The interval timer
 */
export function startPollingLoop(notification: NotificationService): NodeJS.Timeout {
  console.log(`Starting weather data polling (interval: ${config.POLL_RATE}s)`);

  // Run immediately on start
  pollWeatherData(notification).catch((error) => {
    console.error('Initial poll failed:', error);
  });

  // Then run at intervals
  return setInterval(async () => {
    try {
      await pollWeatherData(notification);
    } catch (error) {
      console.error('Polling loop error:', error);
    }
  }, config.POLL_RATE * 1000);
}

/**
 * Stop the polling loop
 *
 * @param {NodeJS.Timeout} timer - The interval timer to stop
 */
export function stopPollingLoop(timer: NodeJS.Timeout): void {
  clearInterval(timer);
  console.log('Stopped weather data polling');
}

/**
 * Get count of pending records in queue
 *
 * @returns {Promise<number>} Number of pending records
 */
export async function getPendingCount(): Promise<number> {
  return await WeatherRecord.countDocuments({ status: 'pending' });
}

/**
 * Get queue statistics
 *
 * @returns {Promise<object>} Queue statistics
 */
export async function getQueueStats(): Promise<{
  pending: number;
  processing: number;
  completed: number;
  failed: number;
}> {
  const [pending, processing, completed, failed] = await Promise.all([
    WeatherRecord.countDocuments({ status: 'pending' }),
    WeatherRecord.countDocuments({ status: 'processing' }),
    WeatherRecord.countDocuments({ status: 'completed' }),
    WeatherRecord.countDocuments({ status: 'failed' }),
  ]);

  return { pending, processing, completed, failed };
}

import { getFundingOutputCount, createFundingOutputs } from './setup';
import { config } from '../config/env';
import { NotificationService } from '../notification/interface';

/**
 * Check the funding basket and refill if needed
 *
 * @param {NotificationService} notification - Notification service for alerts
 * @returns {Promise<void>}
 */
export async function checkFundingBasket(notification: NotificationService): Promise<void> {
  try {
    const count = await getFundingOutputCount();

    if (count < config.FUNDING_BASKET_MIN) {
      await notification.sendWarning(
        `Funding basket low: ${count} outputs (min: ${config.FUNDING_BASKET_MIN})`
      );

      try {
        await createFundingOutputs(config.FUNDING_BATCH_SIZE);
        await notification.sendInfo(
          `Refilled funding basket: created ${config.FUNDING_BATCH_SIZE} outputs`
        );
      } catch (error) {
        if (error instanceof Error && error.message.includes('INSUFFICIENT_FUNDS')) {
          await notification.sendError(
            'CRITICAL: Insufficient funds to create funding outputs. Please add satoshis to wallet.'
          );
        } else {
          await notification.sendError(
            `Failed to refill funding basket: ${error instanceof Error ? error.message : 'Unknown error'}`
          );
        }
        throw error;
      }
    }
  } catch (error) {
    console.error('Error checking funding basket:', error);
    throw error;
  }
}

/**
 * Start the monitoring loop
 * Checks funding basket at configured interval
 *
 * @param {NotificationService} notification - Notification service for alerts
 * @returns {NodeJS.Timeout} The interval timer
 */
export function startMonitoringLoop(notification: NotificationService): NodeJS.Timeout {
  console.log(`Starting funding basket monitor (interval: ${config.MONITOR_INTERVAL}s)`);

  // Run immediately on start
  checkFundingBasket(notification).catch((error) => {
    console.error('Initial funding check failed:', error);
  });

  // Then run at intervals
  return setInterval(async () => {
    try {
      await checkFundingBasket(notification);
    } catch (error) {
      console.error('Monitoring loop error:', error);
    }
  }, config.MONITOR_INTERVAL * 1000);
}

/**
 * Stop the monitoring loop
 *
 * @param {NodeJS.Timeout} timer - The interval timer to stop
 */
export function stopMonitoringLoop(timer: NodeJS.Timeout): void {
  clearInterval(timer);
  console.log('Stopped funding basket monitor');
}

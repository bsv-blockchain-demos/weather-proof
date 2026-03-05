import { Server } from 'http';
import { connectMongo, disconnectMongo } from './db/connection';
import { getWallet } from './service/wallet';
import { ensureFundingOutputs } from './service/setup';
import { startMonitoringLoop, stopMonitoringLoop } from './service/monitor';
import { startPollingLoop, stopPollingLoop, getQueueStats } from './service/queue';
import { startProcessorLoop, stopProcessorLoop, recoverStuckRecords } from './service/processor';
import { ConsoleNotification } from './notification/console';
import { config, validateConfig } from './config/env';
import { startApiServer } from './api';

/**
 * Application state
 */
interface AppState {
  monitorTimer?: NodeJS.Timeout;
  pollingTimer?: NodeJS.Timeout;
  processorTimer?: NodeJS.Timeout;
  apiServer?: Server;
  isShuttingDown: boolean;
}

const state: AppState = {
  isShuttingDown: false,
};

/**
 * Initialize the application
 */
async function initialize(): Promise<void> {
  console.log('='.repeat(60));
  console.log('WeatherProof - BSV Blockchain Weather Data Service');
  console.log('='.repeat(60));

  // Validate configuration
  console.log('Validating configuration...');
  validateConfig();
  console.log('✓ Configuration valid');

  // Connect to MongoDB
  console.log('Connecting to MongoDB...');
  await connectMongo();
  console.log('✓ Connected to MongoDB');

  // Initialize wallet
  console.log('Initializing wallet...');
  await getWallet();
  console.log('✓ Wallet initialized');

  // Recover any records that were stuck mid-processing before the last shutdown
  await recoverStuckRecords();

  // Ensure funding basket
  console.log('Checking funding basket...');
  await ensureFundingOutputs();
  console.log('✓ Funding basket ready');

  // Start API server
  console.log('Starting API server...');
  state.apiServer = await startApiServer();

  console.log('='.repeat(60));
}

/**
 * Start all services
 */
function startServices(): void {
  const notification = new ConsoleNotification();

  console.log('Starting services...');

  // Start monitoring loop
  state.monitorTimer = startMonitoringLoop(notification);

  // Start polling loop
  state.pollingTimer = startPollingLoop(notification);

  // Start processor loop
  state.processorTimer = startProcessorLoop(notification);

  console.log('✓ All services started');
  console.log('='.repeat(60));
}

/**
 * Display status information periodically
 */
function startStatusDisplay(): NodeJS.Timeout {
  return setInterval(async () => {
    try {
      const stats = await getQueueStats();
      console.log(
        `[STATUS] Queue: ${stats.pending} pending, ${stats.processing} processing, ${stats.completed} completed, ${stats.failed} failed`
      );
    } catch (error) {
      console.error('Failed to get status:', error);
    }
  }, 60000); // Every 60 seconds
}

/**
 * Graceful shutdown
 */
async function shutdown(): Promise<void> {
  if (state.isShuttingDown) {
    return;
  }

  state.isShuttingDown = true;

  console.log('\n' + '='.repeat(60));
  console.log('Shutting down gracefully...');
  console.log('='.repeat(60));

  // Stop all services
  if (state.monitorTimer) {
    stopMonitoringLoop(state.monitorTimer);
  }

  if (state.pollingTimer) {
    stopPollingLoop(state.pollingTimer);
  }

  if (state.processorTimer) {
    stopProcessorLoop(state.processorTimer);
  }

  // Close API server
  if (state.apiServer) {
    await new Promise<void>((resolve) => {
      state.apiServer!.close(() => {
        console.log('✓ API server closed');
        resolve();
      });
    });
  }

  // Disconnect from MongoDB
  try {
    await disconnectMongo();
    console.log('✓ Disconnected from MongoDB');
  } catch (error) {
    console.error('Error disconnecting from MongoDB:', error);
  }

  console.log('✓ Shutdown complete');
  process.exit(0);
}

/**
 * Main application entry point
 */
async function main(): Promise<void> {
  try {
    // Initialize
    await initialize();

    // Start services
    startServices();

    // Start status display
    const statusTimer = startStatusDisplay();

    // Handle shutdown signals
    process.on('SIGTERM', () => {
      clearInterval(statusTimer);
      shutdown();
    });

    process.on('SIGINT', () => {
      clearInterval(statusTimer);
      shutdown();
    });

    // Handle uncaught errors
    process.on('unhandledRejection', (reason, promise) => {
      console.error('Unhandled Rejection at:', promise, 'reason:', reason);
    });

    process.on('uncaughtException', (error) => {
      console.error('Uncaught Exception:', error);
      shutdown();
    });

    console.log('Application running. Press Ctrl+C to stop.');
    console.log('='.repeat(60));
  } catch (error) {
    console.error('Failed to start application:', error);
    process.exit(1);
  }
}

// Run the application
if (require.main === module) {
  main().catch((error) => {
    console.error('Fatal error:', error);
    process.exit(1);
  });
}

export { main, shutdown };

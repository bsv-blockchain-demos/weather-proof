import dotenv from 'dotenv';

// Load environment variables
dotenv.config();

/**
 * Environment configuration with validation
 */
export const config = {
  // Wallet Configuration
  SERVER_PRIVATE_KEY: process.env.SERVER_PRIVATE_KEY ?? 'bcc56b658e5b8660ceba47f323e8a77c4794ab9c76f2bd0082a056c723980049',
  WALLET_STORAGE_URL: process.env.WALLET_STORAGE_URL ?? 'https://store-us-1.bsvb.tech',
  BSV_NETWORK: (process.env.BSV_NETWORK?.length ?? 0) > 0 ? (process.env.BSV_NETWORK as 'main' | 'test') : 'test',

  // Tempest API Configuration
  TEMPEST_API_KEY: process.env.TEMPEST_API_KEY ?? '',

  // Polling Configuration
  POLL_RATE: parseInt(process.env.POLL_RATE ?? '300', 10), // seconds

  // MongoDB Configuration
  MONGO_URI: process.env.MONGO_URI ?? 'mongodb://localhost:27017/weather-chain',

  // Funding Configuration
  FUNDING_OUTPUT_AMOUNT: parseInt(process.env.FUNDING_OUTPUT_AMOUNT ?? '1000', 10), // satoshis
  FUNDING_BASKET_MIN: parseInt(process.env.FUNDING_BASKET_MIN ?? '200', 10),
  FUNDING_BATCH_SIZE: parseInt(process.env.FUNDING_BATCH_SIZE ?? '1000', 10),

  // Transaction Configuration
  WEATHER_OUTPUTS_PER_TX: parseInt(process.env.WEATHER_OUTPUTS_PER_TX ?? '100', 10),

  // Service Configuration
  MONITOR_INTERVAL: parseInt(process.env.MONITOR_INTERVAL ?? '60', 10), // seconds
  PROCESSOR_INTERVAL: parseInt(process.env.PROCESSOR_INTERVAL ?? '3', 10), // seconds
};

/**
 * Validate required environment variables
 */
export function validateConfig(): void {
  const errors: string[] = [];

  if (!config.TEMPEST_API_KEY) {
    errors.push('TEMPEST_API_KEY is required');
  }

  if (!config.MONGO_URI) {
    errors.push('MONGO_URI is required');
  }

  if (config.POLL_RATE < 1) {
    errors.push('POLL_RATE must be at least 1 second');
  }

  if (config.FUNDING_OUTPUT_AMOUNT < 100) {
    errors.push('FUNDING_OUTPUT_AMOUNT must be at least 100 satoshis');
  }

  if (config.FUNDING_BASKET_MIN < 10) {
    errors.push('FUNDING_BASKET_MIN must be at least 10');
  }

  if (config.FUNDING_BATCH_SIZE < 1) {
    errors.push('FUNDING_BATCH_SIZE must be at least 1');
  }

  if (config.WEATHER_OUTPUTS_PER_TX < 1 || config.WEATHER_OUTPUTS_PER_TX > 100) {
    errors.push('WEATHER_OUTPUTS_PER_TX must be between 1 and 100');
  }

  if (errors.length > 0) {
    throw new Error(`Configuration validation failed:\n${errors.join('\n')}`);
  }
}

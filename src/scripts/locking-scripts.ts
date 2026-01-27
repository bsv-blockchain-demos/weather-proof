import { Script, OP } from '@bsv/sdk';
import { WeatherData } from '../format/types';
import { WeatherDataEncoder } from '../format/encoder';

/**
 * Create a weather data locking script
 * Format: OP_FALSE OP_RETURN <encoded weather data>
 *
 * @param {WeatherData} data - The weather data to encode
 * @returns {string} The locking script in hex format
 */
export function createWeatherDataLockingScript(data: WeatherData): string {
  const encoder = new WeatherDataEncoder();
  return encoder.encode(data).toHex();
}

/**
 * Extract weather data from a locking script
 * Useful for verifying data on the blockchain
 *
 * @param {string} lockingScriptHex - The locking script in hex format
 * @returns {WeatherData} The decoded weather data
 */
export function extractWeatherDataFromScript(lockingScriptHex: string): WeatherData {
  const { WeatherDataDecoder } = require('../format/decoder');
  const decoder = new WeatherDataDecoder();
  const script = Script.fromHex(lockingScriptHex);
  return decoder.decode(script);
}

/**
 * Verify that a script contains valid weather data
 *
 * @param {string} lockingScriptHex - The locking script in hex format
 * @returns {boolean} True if the script contains valid weather data
 */
export function isValidWeatherDataScript(lockingScriptHex: string): boolean {
  try {
    extractWeatherDataFromScript(lockingScriptHex);
    return true;
  } catch (error) {
    return false;
  }
}

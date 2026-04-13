import { Script, OP, Hash } from '@bsv/sdk';
import { WeatherData } from './types';
import { FIELD_SCHEMA } from './schema';
import { VERSION, FLOAT_SCALE } from './constants';
import { encodeFloat } from '../utils/float-encoder';

/**
 * Encoder for weather data into Bitcoin Script format.
 * Converts WeatherData objects into blockchain-ready scripts where each field
 * is pushed as a separate chunk onto the stack.
 */
export class WeatherDataEncoder {
  private readonly version: number = VERSION;
  private readonly floatScale: number = FLOAT_SCALE;

  /**
   * Encodes weather data into a Bitcoin Script.
   * The encoding follows the fixed schema with version byte first,
   * followed by all fields in schema order.
   *
   * @param data - The weather data to encode
   * @returns A Script object containing the encoded data
   *
   * @example
   * const encoder = new WeatherDataEncoder();
   * const script = encoder.encode(weatherData);
   * console.log(script.toHex());
   */
  encode(data: WeatherData): Script {
    const script = new Script();

    // data only marker for now
    script.writeOpCode(OP.OP_FALSE)
    script.writeOpCode(OP.OP_RETURN)

    // Push version byte
    script.writeNumber(this.version);

    // Iterate through fields in schema order
    for (const field of FIELD_SCHEMA) {
      const value = data[field.name];

      switch (field.type) {
        case 'integer':
          script.writeNumber(value as number);
          break;

        case 'float':
          const scaled = encodeFloat(value as number, this.floatScale);
          script.writeNumber(scaled);
          break;

        case 'string':
          const utf8Bytes = Buffer.from(value as string, 'utf8');
          script.writeBin(Array.from(utf8Bytes));
          break;

        case 'boolean':
          script.writeNumber((value as boolean) ? 1 : 0);
          break;
      }
    }

    return script;
  }

  /**
   * Encodes weather data into a hex string.
   * Convenience method that combines encode() and toHex().
   *
   * @param data - The weather data to encode
   * @returns A hex string representation of the encoded script
   */
  encodeToHex(data: WeatherData): string {
    return this.encode(data).toHex();
  }

  /**
   * Encodes weather data as an OP_RETURN script containing only the SHA-256
   * hash of the full encoded data, rather than the data itself.
   *
   * @param data - The weather data to encode
   * @returns A Script with OP_FALSE OP_RETURN <sha256(encoded_data)>
   */
  encodeHash(data: WeatherData): Script {
    const hash = Hash.sha256(JSON.stringify(data));
    const script = new Script();
    script.writeOpCode(OP.OP_FALSE);
    script.writeOpCode(OP.OP_RETURN);
    script.writeBin(hash);
    return script;
  }
}

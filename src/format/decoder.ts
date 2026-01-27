import { Script } from '@bsv/sdk';
import { WeatherData } from './types';
import { FIELD_SCHEMA } from './schema';
import { VERSION, FLOAT_SCALE } from './constants';
import { decodeFloat } from '../utils/float-encoder';

/**
 * Decoder for weather data from Bitcoin Script format.
 * Reconstructs WeatherData objects from blockchain scripts.
 */
export class WeatherDataDecoder {
  private readonly floatScale: number = FLOAT_SCALE;

  /**
   * Decodes a Bitcoin Script back into weather data.
   * Reads the version byte first, then all fields in schema order.
   *
   * @param script - The Script object to decode
   * @returns The decoded WeatherData object
   * @throws Error if version is unsupported or script is malformed
   *
   * @example
   * const decoder = new WeatherDataDecoder();
   * const data = decoder.decode(script);
   * console.log(data.conditions);
   */
  decode(script: Script): WeatherData {
    const chunks = script.chunks;
    let index = 2;

    // Read and validate version
    const version = this.readNumber(chunks[index++]);
    if (version !== VERSION) {
      throw new Error(`Unsupported version: ${version}. Expected: ${VERSION}`);
    }

    // Ensure we have enough chunks for all fields
    const expectedChunks = 1 + FIELD_SCHEMA.length; // version + all fields
    if (chunks.length < expectedChunks) {
      throw new Error(
        `Malformed script: expected at least ${expectedChunks} chunks, got ${chunks.length}`
      );
    }

    const result: Partial<WeatherData> = {};

    // Read fields in schema order
    for (const field of FIELD_SCHEMA) {
      const chunk = chunks[index++];

      switch (field.type) {
        case 'integer':
          result[field.name] = this.readNumber(chunk) as any;
          break;

        case 'float':
          const scaled = this.readNumber(chunk);
          result[field.name] = decodeFloat(scaled, this.floatScale) as any;
          break;

        case 'string':
          result[field.name] = this.readString(chunk) as any;
          break;

        case 'boolean':
          result[field.name] = (this.readNumber(chunk) === 1) as any;
          break;
      }
    }
    
    return result as WeatherData;
  }

  /**
   * Decodes a hex string back into weather data.
   * Convenience method that combines Script.fromHex() and decode().
   *
   * @param hex - The hex string representation of the encoded script
   * @returns The decoded WeatherData object
   */
  decodeFromHex(hex: string): WeatherData {
    return this.decode(Script.fromHex(hex));
  }

  /**
   * Reads a number from a script chunk.
   * Handles Bitcoin Script integer encoding including opcodes.
   *
   * @param chunk - The script chunk to read from
   * @returns The decoded number
   */
  private readNumber(chunk: any): number {
    // Handle opcode numbers (OP_0, OP_1 through OP_16, OP_1NEGATE)
    if (chunk.op !== undefined) {
      const op = chunk.op;

      // OP_0
      if (op === 0x00) {
        return 0;
      }

      // OP_1NEGATE
      if (op === 0x4f) {
        return -1;
      }

      // OP_1 through OP_16
      if (op >= 0x51 && op <= 0x60) {
        return op - 0x50;
      }
    }

    // Handle data pushes
    if (chunk.data && chunk.data.length > 0) {
      return this.bytesToNumber(Array.from(chunk.data));
    }

    // Empty data means 0
    return 0;
  }

  /**
   * Reads a string from a script chunk.
   * Decodes UTF-8 bytes back to string.
   *
   * @param chunk - The script chunk to read from
   * @returns The decoded string
   */
  private readString(chunk: any): string {
    if (!chunk.data || chunk.data.length === 0) {
      return '';
    }
    return Buffer.from(chunk.data).toString('utf8');
  }

  /**
   * Converts little-endian bytes to a number.
   * Follows Bitcoin Script integer encoding rules.
   *
   * @param bytes - The byte array in little-endian format
   * @returns The decoded number
   */
  private bytesToNumber(bytes: number[]): number {
    if (bytes.length === 0) {
      return 0;
    }

    // Check if negative (most significant bit of last byte)
    const negative = (bytes[bytes.length - 1] & 0x80) !== 0;

    // Remove the sign bit for calculation
    let result = 0;
    const lastByteIndex = bytes.length - 1;

    for (let i = 0; i < bytes.length; i++) {
      let byte = bytes[i];

      // Clear the sign bit on the last byte
      if (i === lastByteIndex && negative) {
        byte = byte & 0x7f;
      }

      result += byte * Math.pow(256, i);
    }

    return negative ? -result : result;
  }
}

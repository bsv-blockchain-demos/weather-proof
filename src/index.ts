/**
 * WeatherProof - Bitcoin Script encoding system for weather data
 *
 * This library provides utilities to encode weather data into Bitcoin Script format
 * for storage on the BSV blockchain. Each field is pushed as a separate chunk onto
 * the stack, enabling smart contracts to read and process the data.
 *
 * @packageDocumentation
 */

// Core encoder/decoder
export { WeatherDataEncoder } from './format/encoder';
export { WeatherDataDecoder } from './format/decoder';

// Types
export type { WeatherData, FieldType, FieldDefinition } from './format/types';

// Schema and constants
export { FIELD_SCHEMA } from './format/schema';
export { VERSION, FLOAT_SCALE, FLOAT_EPSILON } from './format/constants';

// Utilities
export { encodeFloat, decodeFloat, validateFloatPrecision } from './utils/float-encoder';

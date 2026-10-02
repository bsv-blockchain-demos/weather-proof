# Weather Chain - Usage Guide

Bitcoin Script encoding system for weather data on the BSV blockchain.

## Installation

```bash
npm install
npm run build
```

## Quick Start

```typescript
import { WeatherDataEncoder, WeatherDataDecoder, WeatherData } from './src';

// Create encoder and decoder instances
const encoder = new WeatherDataEncoder();
const decoder = new WeatherDataDecoder();

// Your weather data
const weatherData: WeatherData = {
  air_density: 1.29,
  air_temperature: -9,
  brightness: 68055,
  conditions: 'Clear',
  delta_t: 2,
  dew_point: -17,
  feels_like: -13,
  icon: 'clear-day',
  is_precip_local_day_rain_check: true,
  is_precip_local_yesterday_rain_check: true,
  lightning_strike_count_last_1hr: 0,
  lightning_strike_count_last_3hr: 0,
  lightning_strike_last_distance: 32,
  lightning_strike_last_distance_msg: '30 - 34 km',
  lightning_strike_last_epoch: 1761103981,
  precip_accum_local_day: 0,
  precip_accum_local_yesterday: 0,
  precip_minutes_local_day: 0,
  precip_minutes_local_yesterday: 0,
  precip_probability: 0,
  pressure_trend: 'falling',
  relative_humidity: 49,
  sea_level_pressure: 1019,
  solar_radiation: 567,
  station_pressure: 979.7,
  time: 1769529302,
  uv: 2,
  wet_bulb_globe_temperature: -9,
  wet_bulb_temperature: -11,
  wind_avg: 2,
  wind_direction: 280,
  wind_direction_cardinal: 'W',
  wind_gust: 4,
};

// Encode to Bitcoin Script
const script = encoder.encode(weatherData);

// Get hex representation for blockchain storage
const hexScript = script.toHex();
console.log('Encoded script (hex):', hexScript);
console.log('Script size:', script.toBinary().length, 'bytes');

// Later, decode from hex
const decodedData = decoder.decodeFromHex(hexScript);
console.log('Decoded data:', decodedData);

// Verify integrity
console.log('Time matches:', decodedData.time === weatherData.time);
console.log('Conditions match:', decodedData.conditions === weatherData.conditions);
```

## API Reference

### WeatherDataEncoder

Encodes weather data into Bitcoin Script format.

#### Methods

- `encode(data: WeatherData): Script` - Encodes weather data into a Script object
- `encodeToHex(data: WeatherData): string` - Encodes weather data directly to hex string

### WeatherDataDecoder

Decodes Bitcoin Script back into weather data.

#### Methods

- `decode(script: Script): WeatherData` - Decodes a Script object back to weather data
- `decodeFromHex(hex: string): WeatherData` - Decodes a hex string directly to weather data

## Encoding Format

The system uses a fixed schema approach for maximum efficiency:

1. **Version byte** (1 byte) - Enables schema evolution
2. **Field data** (variable) - All 33 fields in fixed order

### Data Type Encoding

- **Integers**: Bitcoin Script native integer encoding (little-endian)
- **Floats**: Fixed-point encoding with 10^6 scale factor (6 decimal precision)
- **Strings**: UTF-8 bytes with automatic OP_PUSHDATA opcodes
- **Booleans**: 0 (false) or 1 (true)

## Field Order

Fields are encoded in the following order (from `schema.ts`):

1. time (integer) - Unix timestamp
2. Temperature fields (7 integers)
3. Pressure fields (2 float + 1 integer + 1 string)
4. Humidity fields (2 integer + 1 float)
5. Wind fields (5 integers + 1 string)
6. Precipitation fields (7 integers + 2 booleans)
7. Lightning fields (5 integers + 1 string)
8. Solar/visibility fields (3 integers)
9. Conditions (2 strings)

## Script Size

The encoded script is highly efficient:

- **Example data**: 97 bytes
- **Hex length**: 194 characters
- **Chunks**: 34 (version + 33 fields)

This compact size minimizes blockchain storage costs.

## Float Precision

Floats are encoded with 6 decimal places of precision:

- **Scale factor**: 1,000,000
- **Example**: 1.29 → 1,290,000 (as integer)
- **Epsilon**: 0.000001 (1e-6)

This provides sufficient precision for weather data while maintaining integer-only operations on-chain.

## Testing

Run the comprehensive test suite:

```bash
npm test                 # Run all tests (111 tests)
npm run test:watch       # Watch mode
npm run test:coverage    # Coverage report
```

### Test Coverage

- Statement coverage: 98%
- Branch coverage: 93.5%
- Line coverage: 97.8%
- All 111 tests passing

## Verification

The system ensures **zero data loss** through:

1. Comprehensive unit tests for each data type
2. Round-trip tests (encode → decode → verify)
3. Edge case tests (extreme values, unicode, etc.)
4. Integration tests with real weather data

## Usage in Smart Contracts

Once encoded to hex, the script can be:

1. Stored in a Bitcoin transaction output
2. Read by smart contracts on-chain
3. Each field accessible as a separate stack element
4. Type information known from schema version

Example smart contract pseudocode:

```
// Script on-chain has weather data as stack elements
OP_DUP            # Duplicate top element
OP_1              # Push version check
OP_EQUALVERIFY    # Verify version
# Now stack has all 33 weather fields
# Smart contract can process each field
```

## Licence

**Open BSV Licence v6.** See [LICENSE.txt](LICENSE.txt) for the full terms. The licence applies to this project's original code and documentation and restricts use to the BSV blockchain defined in the licence. Third-party code, assets and referenced standards retain their respective terms.

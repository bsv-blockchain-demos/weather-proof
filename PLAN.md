# Weather Chain Implementation Plan

## Overview

This document outlines the implementation plan for a weather data processing application that:
1. Sources live weather data from the Tempest API
2. Encodes it using the existing WeatherDataEncoder
3. Stores it on the BSV blockchain as OP_RETURN outputs
4. Uses a funding basket with hash puzzle UTXOs for transaction fees

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Weather Chain Service                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐                   │
│  │   Tempest    │───▶│    Queue     │───▶│  Processor   │                   │
│  │   Poller     │    │  (MongoDB)   │    │   Service    │                   │
│  └──────────────┘    └──────────────┘    └──────┬───────┘                   │
│         │                                        │                           │
│         │ POLL_RATE                              │                           │
│         │ (300s)                                 ▼                           │
│         │                              ┌──────────────┐                      │
│         │                              │   Wallet     │                      │
│         │                              │   Service    │                      │
│         │                              └──────┬───────┘                      │
│         │                                     │                              │
│         │                                     ▼                              │
│         │                              ┌──────────────┐                      │
│         │                              │  Blockchain  │                      │
│         │                              │  (BSV)       │                      │
│         │                              └──────────────┘                      │
│         │                                                                    │
│  ┌──────▼───────┐                      ┌──────────────┐                      │
│  │   Monitor    │◀────────────────────▶│   Funding    │                      │
│  │   Service    │                      │   Basket     │                      │
│  └──────────────┘                      └──────────────┘                      │
│         │                                                                    │
│         ▼                                                                    │
│  ┌──────────────┐                                                            │
│  │ Notification │                                                            │
│  │   Service    │                                                            │
│  └──────────────┘                                                            │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `SERVER_PRIVATE_KEY` | Hex-encoded private key for wallet | (test key) |
| `WALLET_STORAGE_URL` | Wallet storage provider URL | `https://store-us-1.bsvb.tech` |
| `BSV_NETWORK` | Network (`main` or `test`) | `test` |
| `TEMPEST_API_KEY` | API key for Tempest weather API | (required) |
| `POLL_RATE` | Polling interval in seconds | `300` |
| `MONGO_URI` | MongoDB connection string | (required) |
| `FUNDING_OUTPUT_AMOUNT` | Satoshis per funding output | `1000` |
| `FUNDING_BASKET_MIN` | Minimum funding outputs before refill | `200` |
| `FUNDING_BATCH_SIZE` | Outputs to create per refill | `1000` |
| `WEATHER_OUTPUTS_PER_TX` | Weather outputs per transaction | `100` |

## Directory Structure

```
src/
├── index.ts                    # Main exports (existing)
├── config/
│   └── env.ts                  # Environment configuration
├── format/                     # Existing encoder/decoder
│   ├── encoder.ts
│   ├── decoder.ts
│   ├── schema.ts
│   ├── types.ts
│   └── constants.ts
├── service/
│   ├── wallet.ts               # Existing wallet service
│   ├── setup.ts                # Funding basket setup
│   ├── monitor.ts              # Funding basket monitor
│   ├── tempest.ts              # Tempest API client
│   ├── queue.ts                # Data queue service
│   ├── processor.ts            # Transaction processor
│   └── transaction.ts          # Transaction creation
├── notification/
│   ├── interface.ts            # Notification interface
│   ├── console.ts              # Console logger (default)
│   └── twilio.ts               # Twilio SMS (optional)
├── db/
│   ├── connection.ts           # MongoDB connection
│   └── models/
│       └── weather-record.ts   # Weather data model
├── scripts/
│   ├── hash-puzzle.ts          # Hash puzzle utilities
│   └── locking-scripts.ts      # Script builders
└── app.ts                      # Main application entry
```

## Implementation Phases

### Phase 1: Configuration & Dependencies

**Files to create:**
- `src/config/env.ts`

**Dependencies to add:**
- `@bsv/wallet-toolbox-client` (wallet toolbox)
- `mongoose` (MongoDB ODM)
- `dotenv` (environment variables)

**Tasks:**
1. Add dependencies to package.json
2. Create environment configuration with validation
3. Create `.env.example` template

### Phase 2: Notification System

**Files to create:**
- `src/notification/interface.ts`
- `src/notification/console.ts`
- `src/notification/twilio.ts`

**Interface Definition:**
```typescript
export interface NotificationService {
  sendWarning(message: string): Promise<void>;
  sendError(message: string): Promise<void>;
  sendInfo(message: string): Promise<void>;
}
```

**Tasks:**
1. Define notification interface
2. Implement console logger (default)
3. Implement Twilio SMS adapter (optional)

### Phase 3: Database Layer

**Files to create:**
- `src/db/connection.ts`
- `src/db/models/weather-record.ts`

**MongoDB Schema:**
```typescript
interface WeatherRecord {
  _id: ObjectId;
  stationId: number;
  timestamp: Date;
  data: WeatherData;        // From existing types
  status: 'pending' | 'processing' | 'completed' | 'failed';
  txid?: string;
  outputIndex?: number;
  error?: string;
  createdAt: Date;
  processedAt?: Date;
}
```

**Tasks:**
1. Create MongoDB connection manager
2. Define WeatherRecord schema and model
3. Add indexes for efficient queries

### Phase 4: Hash Puzzle Scripts

**Files to create:**
- `src/scripts/hash-puzzle.ts`
- `src/scripts/locking-scripts.ts`

**Hash Puzzle Implementation:**
```typescript
import { Random, Hash, Script, OP } from '@bsv/sdk';

export function createHashPuzzle(): { lockingScript: string; preimage: string } {
  const preimage = Random(32);  // 32 random bytes
  const hash = Hash.sha256(preimage);

  // Locking script: OP_SHA256 <hash> OP_EQUAL
  const lockingScript = new Script();
  lockingScript.writeOpCode(OP.OP_SHA256);
  lockingScript.writeBin(Array.from(hash));
  lockingScript.writeOpCode(OP.OP_EQUAL);

  return {
    lockingScript: lockingScript.toHex(),
    preimage: Buffer.from(preimage).toString('hex')
  };
}

export function createUnlockingScript(preimage: string): string {
  const script = new Script();
  script.writeBin(Array.from(Buffer.from(preimage, 'hex')));
  return script.toHex();
}
```

**Tasks:**
1. Implement hash puzzle creation
2. Implement unlocking script generation
3. Add unit tests for hash puzzle scripts

### Phase 5: Setup Service

**Files to create:**
- `src/service/setup.ts`

**Functionality:**
1. Create funding outputs in batches of 1000
2. Each output uses a hash puzzle locking script
3. Store preimage in `customInstructions` for later retrieval
4. Use basket name: `"funding"`

**Key Method:**
```typescript
async function createFundingOutputs(count: number = 1000): Promise<void> {
  const wallet = await getWallet();

  const outputs = [];
  for (let i = 0; i < count; i++) {
    const { lockingScript, preimage } = createHashPuzzle();
    outputs.push({
      satoshis: FUNDING_OUTPUT_AMOUNT,
      lockingScript,
      basket: 'funding',
      outputDescription: 'funding output',
      customInstructions: preimage  // Store preimage for unlocking
    });
  }

  await wallet.createAction({
    description: 'Create funding outputs',
    outputs
  });
}
```

**Tasks:**
1. Implement funding output creation
2. Handle transaction size limits (batch if needed)
3. Add error handling for insufficient funds

### Phase 6: Monitoring Service

**Files to create:**
- `src/service/monitor.ts`

**Functionality:**
1. Periodically check funding basket count
2. Trigger refill when below threshold (200)
3. Send notifications on errors

**Key Method:**
```typescript
async function checkFundingBasket(): Promise<void> {
  const wallet = await getWallet();

  const { outputs } = await wallet.listOutputs({
    basket: 'funding',
    spendable: true
  });

  if (outputs.length < FUNDING_BASKET_MIN) {
    try {
      await createFundingOutputs(FUNDING_BATCH_SIZE);
      await notification.sendInfo(`Created ${FUNDING_BATCH_SIZE} funding outputs`);
    } catch (error) {
      if (error.message.includes('insufficient funds')) {
        await notification.sendWarning('Insufficient funds to create funding outputs');
      }
      throw error;
    }
  }
}
```

**Tasks:**
1. Implement periodic monitoring loop
2. Integrate with notification service
3. Handle edge cases and errors

### Phase 7: Tempest API Client

**Files to create:**
- `src/service/tempest.ts`

**API Endpoints:**
1. Get stations: `GET https://swd.weatherflow.com/swd/rest/stations?token=${token}`
2. Get forecast: `GET https://swd.weatherflow.com/swd/rest/better_forecast?station_id=${id}&token=${token}`

**Key Methods:**
```typescript
async function getStations(): Promise<number[]> {
  const response = await fetch(
    `https://swd.weatherflow.com/swd/rest/stations?token=${TEMPEST_API_KEY}`
  );
  const data = await response.json();
  return data.stations.map((s: any) => s.station_id);
}

async function getCurrentConditions(stationId: number): Promise<WeatherData> {
  const response = await fetch(
    `https://swd.weatherflow.com/swd/rest/better_forecast?station_id=${stationId}&token=${TEMPEST_API_KEY}`
  );
  const data = await response.json();
  return mapToWeatherData(data.current_conditions);
}
```

**Tasks:**
1. Implement station listing
2. Implement forecast fetching
3. Map API response to WeatherData type
4. Add error handling and retries

### Phase 8: Data Queue Service

**Files to create:**
- `src/service/queue.ts`

**Functionality:**
1. Poll Tempest API at configured interval
2. Store raw data in MongoDB with 'pending' status
3. Handle API errors gracefully

**Key Method:**
```typescript
async function pollWeatherData(): Promise<void> {
  const stations = await getStations();

  for (const stationId of stations) {
    try {
      const data = await getCurrentConditions(stationId);

      await WeatherRecord.create({
        stationId,
        timestamp: new Date(),
        data,
        status: 'pending',
        createdAt: new Date()
      });
    } catch (error) {
      await notification.sendError(`Failed to fetch data for station ${stationId}: ${error.message}`);
    }
  }
}
```

**Tasks:**
1. Implement polling loop with configurable interval
2. Store records in MongoDB
3. Handle partial failures (continue with other stations)

### Phase 9: Transaction Service

**Files to create:**
- `src/service/transaction.ts`

**Functionality:**
1. Create transactions with weather data outputs
2. Use funding basket inputs
3. Batch multiple weather outputs per transaction (up to 100)

**Key Method:**
```typescript
async function createWeatherTransaction(records: WeatherRecord[]): Promise<{txid: string; outputIndexes: number[]}> {
  const wallet = await getWallet();
  const encoder = new WeatherDataEncoder();

  // Get one funding input
  const { outputs: fundingOutputs, BEEF } = await wallet.listOutputs({
    basket: 'funding',
    spendable: true,
    include: 'entire transactions',
    limit: 1
  });

  if (fundingOutputs.length === 0) {
    throw new Error('No funding outputs available');
  }

  const fundingOutput = fundingOutputs[0];
  const preimage = fundingOutput.customInstructions;

  // Create weather outputs
  const weatherOutputs = records.map(record => {
    const script = encoder.encode(record.data);
    return {
      satoshis: 0,
      lockingScript: script.toHex(),
      outputDescription: 'weather'
    };
  });

  // Create transaction
  const result = await wallet.createAction({
    description: 'Weather data storage',
    inputBEEF: BEEF,
    inputs: [{
      outpoint: fundingOutput.outpoint,
      unlockingScript: createUnlockingScript(preimage)
    }],
    outputs: weatherOutputs
  });

  return {
    txid: result.txid,
    outputIndexes: weatherOutputs.map((_, i) => i)
  };
}
```

**Tasks:**
1. Implement transaction creation
2. Handle funding input retrieval and unlocking
3. Return txid and output indexes for record updates

### Phase 10: Processor Service

**Files to create:**
- `src/service/processor.ts`

**Functionality:**
1. Process pending records from MongoDB
2. Batch records for efficient transaction creation
3. Update records with txid and output index
4. Handle failures gracefully

**Key Method:**
```typescript
async function processPendingRecords(): Promise<void> {
  // Get pending records in batches
  const records = await WeatherRecord.find({ status: 'pending' })
    .sort({ createdAt: 1 })
    .limit(WEATHER_OUTPUTS_PER_TX);

  if (records.length === 0) return;

  // Mark as processing
  const ids = records.map(r => r._id);
  await WeatherRecord.updateMany(
    { _id: { $in: ids } },
    { status: 'processing' }
  );

  try {
    const { txid, outputIndexes } = await createWeatherTransaction(records);

    // Update records with txid and output index
    for (let i = 0; i < records.length; i++) {
      await WeatherRecord.updateOne(
        { _id: records[i]._id },
        {
          status: 'completed',
          txid,
          outputIndex: outputIndexes[i],
          processedAt: new Date()
        }
      );
    }
  } catch (error) {
    // Mark as failed
    await WeatherRecord.updateMany(
      { _id: { $in: ids } },
      { status: 'pending', error: error.message }
    );
    throw error;
  }
}
```

**Tasks:**
1. Implement batch processing loop
2. Update MongoDB records with blockchain references
3. Handle transaction failures

### Phase 11: Main Application

**Files to create:**
- `src/app.ts`

**Functionality:**
1. Initialize all services
2. Run setup if needed
3. Start monitoring and polling loops
4. Handle graceful shutdown

**Main Entry Point:**
```typescript
async function main(): Promise<void> {
  // Initialize
  await connectMongo();
  const wallet = await getWallet();
  const notification = new ConsoleNotification();

  // Check/create initial funding
  await ensureFundingBasket();

  // Start services
  startMonitoringLoop();      // Check funding every 60s
  startPollingLoop();         // Poll Tempest every POLL_RATE
  startProcessorLoop();       // Process queue every 3s

  // Handle shutdown
  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);
}
```

**Tasks:**
1. Implement service orchestration
2. Add graceful shutdown handling
3. Add health checks and logging

### Phase 12: Configuration & Documentation

**Files to create:**
- `.env.example`
- Update `package.json` scripts

**Package.json Scripts:**
```json
{
  "scripts": {
    "start": "node dist/app.js",
    "dev": "ts-node src/app.ts",
    "build": "tsc",
    "setup": "ts-node src/scripts/setup-funding.ts",
    "test": "jest"
  }
}
```

**Tasks:**
1. Create .env.example with all variables
2. Add npm scripts
3. Update tsconfig.json if needed

## Transaction Economics

**Funding Output Economics:**
- Each funding output: 1000 satoshis
- Each funding output can fund ~100 weather outputs (0 satoshis each)
- Transaction fee: ~200-500 satoshis per transaction
- Remaining change returns to funding basket or new output

**Processing Rate:**
- Target: 1 transaction every 3 seconds
- Each transaction: up to 100 weather data outputs
- Poll rate: 300 seconds (5 minutes)
- Stations per poll: variable (depends on API access)

**Funding Sustainability:**
- 1000 funding outputs created at a time
- Refill triggered at 200 remaining
- Each funding output supports multiple transactions via change

## Error Handling Strategy

1. **API Failures**: Log and continue with other stations
2. **Transaction Failures**: Mark records as pending for retry
3. **Funding Exhaustion**: Send warning notification, pause processing
4. **MongoDB Failures**: Retry with exponential backoff
5. **Wallet Failures**: Log and alert operator

## Testing Strategy

1. **Unit Tests**: Hash puzzle, encoder integration, data mapping
2. **Integration Tests**: MongoDB operations, wallet operations (testnet)
3. **E2E Tests**: Full flow with mocked Tempest API

## Monitoring & Observability

1. **Metrics**: Records processed, transactions created, funding balance
2. **Alerts**: Low funding, API failures, processing delays
3. **Logs**: Structured JSON logging for all operations

## Critical Files Summary

| File | Purpose |
|------|---------|
| `src/config/env.ts` | Environment configuration |
| `src/service/setup.ts` | Funding basket initialization |
| `src/service/monitor.ts` | Funding level monitoring |
| `src/service/tempest.ts` | Tempest API client |
| `src/service/queue.ts` | Data polling and queuing |
| `src/service/processor.ts` | Queue processing |
| `src/service/transaction.ts` | Blockchain transaction creation |
| `src/scripts/hash-puzzle.ts` | Hash puzzle script utilities |
| `src/db/models/weather-record.ts` | MongoDB model |
| `src/notification/interface.ts` | Notification abstraction |
| `src/app.ts` | Main application entry |

## Verification Steps

1. **Setup Verification**:
   - Run setup script
   - Verify 1000 funding outputs in basket
   - Verify each has customInstructions (preimage)

2. **Polling Verification**:
   - Start application
   - Verify Tempest API calls succeed
   - Verify records appear in MongoDB

3. **Processing Verification**:
   - Verify transactions created on blockchain
   - Verify records updated with txid
   - Verify weather data decodable from blockchain

4. **Monitoring Verification**:
   - Consume funding outputs
   - Verify refill triggers at threshold
   - Verify notifications sent

5. **End-to-End Verification**:
   - Run full system for extended period
   - Verify consistent transaction rate
   - Verify no data loss

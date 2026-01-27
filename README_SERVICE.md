# Weather Chain Service

A BSV blockchain application that sources live weather data from the Tempest API and stores it on the blockchain as OP_RETURN outputs with efficient Bitcoin Script encoding.

## Features

- **Weather Data Encoding**: Lossless encoding of weather data using Bitcoin Script
- **Tempest API Integration**: Automatic polling of weather stations
- **Funding Basket**: Hash puzzle UTXOs for transaction fees
- **MongoDB Queue**: Async processing with failure recovery
- **Auto-refill**: Monitoring service maintains funding levels
- **Notifications**: Console logging with optional Twilio SMS

## Architecture

```
Tempest API → MongoDB Queue → Transaction Processor → BSV Blockchain
                    ↓
            Funding Basket Monitor
```

## Installation

```bash
npm install
```

## Configuration

Copy `.env.example` to `.env` and configure:

```bash
cp .env.example .env
```

Required environment variables:
- `TEMPEST_API_KEY` - Your Tempest weather API key
- `MONGO_URI` - MongoDB connection string

Optional (with defaults):
- `SERVER_PRIVATE_KEY` - BSV private key (hex)
- `WALLET_STORAGE_URL` - Wallet storage provider URL
- `BSV_NETWORK` - Network: `test` or `main`
- `POLL_RATE` - Polling interval in seconds (default: 300)
- `FUNDING_OUTPUT_AMOUNT` - Satoshis per funding output (default: 1000)
- `FUNDING_BASKET_MIN` - Minimum outputs before refill (default: 200)
- `FUNDING_BATCH_SIZE` - Outputs created per refill (default: 1000)
- `WEATHER_OUTPUTS_PER_TX` - Weather outputs per transaction (default: 100)

## Setup

Before running the application, create the initial funding basket:

```bash
npm run setup
```

This will:
1. Connect to the wallet
2. Create hash puzzle funding outputs
3. Store preimages for later unlocking

## Running

Start the application:

```bash
npm run build
npm start
```

Or run in development mode:

```bash
npm run dev
```

The application will:
1. Poll Tempest API every 5 minutes (configurable)
2. Queue weather data in MongoDB
3. Process queue into blockchain transactions
4. Monitor funding basket and auto-refill

## How It Works

### 1. Funding Basket

The funding basket contains hash puzzle outputs that fund weather data transactions:

- Each output has a SHA256 hash puzzle locking script
- Preimage stored in wallet's `customInstructions`
- Unlocking script simply pushes the preimage

### 2. Weather Data Encoding

Weather data is encoded using a fixed schema:

```
OP_FALSE OP_RETURN <version> <field1> <field2> ... <field33>
```

Data types:
- **Integers**: Native Bitcoin Script encoding
- **Floats**: Fixed-point (scale: 10^6, 6 decimal precision)
- **Strings**: UTF-8 bytes
- **Booleans**: 0 or 1

### 3. Data Flow

1. **Polling**: Query Tempest API for all stations
2. **Queuing**: Store raw data in MongoDB with `pending` status
3. **Processing**: Batch pending records (up to 100)
4. **Transaction**: Create transaction with weather outputs
5. **Update**: Store `txid` and `outputIndex` in MongoDB

### 4. Monitoring

The monitor service checks the funding basket every 60 seconds:

- If below 200 outputs: create 1000 more
- If insufficient funds: send alert notification

## Transaction Economics

- **Funding output**: 1000 satoshis
- **Weather output**: 0 satoshis (OP_RETURN data)
- **Transaction fee**: ~200-500 satoshis
- **Outputs per funding input**: ~100 weather outputs
- **Script size**: ~99 bytes per weather data output

## Notifications

### Console (Default)

Logs all notifications to console with timestamps and levels.

### Twilio SMS (Optional)

Add to `.env`:

```bash
TWILIO_ACCOUNT_SID=your_account_sid
TWILIO_AUTH_TOKEN=your_auth_token
TWILIO_FROM_NUMBER=+1234567890
TWILIO_TO_NUMBER=+1234567890
```

Then modify `src/app.ts` to use `TwilioNotification` instead of `ConsoleNotification`.

## License

Open BSV License

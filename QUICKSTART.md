# Weather Chain - Quick Start Guide

## Prerequisites

- Node.js 18+ and npm
- MongoDB instance
- BSV testnet wallet with some satoshis
- Tempest weather API key

## Installation

```bash
# Clone repository
cd weather-chain

# Install dependencies
npm install

# Build project
npm run build
```

## Configuration

Create `.env` file from template:

```bash
cp .env.example .env
```

Edit `.env` with your settings:

```bash
# Required
TEMPEST_API_KEY=your_tempest_api_key_here
MONGO_URI=mongodb://localhost:27017/weather-chain

# Optional (shown with defaults)
SERVER_PRIVATE_KEY=bcc56b658e5b8660ceba47f323e8a77c4794ab9c76f2bd0082a056c723980049
WALLET_STORAGE_URL=https://store-us-1.bsvb.tech
BSV_NETWORK=test
POLL_RATE=300
FUNDING_OUTPUT_AMOUNT=1000
FUNDING_BASKET_MIN=200
FUNDING_BATCH_SIZE=1000
WEATHER_OUTPUTS_PER_TX=100
```

## Setup

Create the initial funding basket (1000 outputs):

```bash
npm run setup
```

This will prompt you for how many outputs to create. The default is 1000.

## Running

Start the service:

```bash
npm start
```

The service will:
- Poll Tempest API every 5 minutes
- Store weather data in MongoDB queue
- Process queue into blockchain transactions
- Monitor funding basket and auto-refill

## Monitoring

Check the console output for status:

```
[STATUS] Queue: 15 pending, 0 processing, 243 completed, 0 failed
```

Check MongoDB for records:

```bash
mongosh mongodb://localhost:27017/weather-chain
> db.weatherrecords.find({status: 'completed'}).limit(5)
```

Check funding basket:

```bash
# The service logs funding status automatically
[INFO] Current funding outputs: 892
```

## Development Mode

Run in development with auto-reload:

```bash
npm run dev
```

## Testing

Run the test suite (111 tests):

```bash
npm test
```

Run with coverage:

```bash
npm run test:coverage
```

## Troubleshooting

### "Insufficient funds to create funding outputs"

Your wallet needs more satoshis. Add funds to the wallet address and retry setup.

### "TEMPEST_API_KEY is required"

Add your Tempest API key to `.env` file.

### "Failed to connect to MongoDB"

Ensure MongoDB is running and MONGO_URI is correct.

### No weather data being processed

1. Check Tempest API key is valid
2. Check MongoDB connection
3. Check wallet has funding outputs
4. Check console logs for specific errors

## Architecture

```
Tempest API (300s poll)
    ↓
MongoDB Queue (pending records)
    ↓
Processor (3s interval, batches of 100)
    ↓
BSV Blockchain (OP_RETURN outputs)

Monitor (60s interval)
    ↓
Funding Basket (hash puzzle UTXOs)
```

## Key Services

- **Polling**: Fetches weather data every 5 minutes
- **Processor**: Creates transactions every 3 seconds
- **Monitor**: Checks funding every 60 seconds
- **Queue**: MongoDB-backed async processing

## Next Steps

1. Monitor the first few transactions on blockchain explorer
2. Verify weather data encoding/decoding works correctly
3. Adjust POLL_RATE and WEATHER_OUTPUTS_PER_TX as needed
4. Set up production monitoring/alerts
5. Consider Twilio SMS notifications for critical alerts

## Documentation

- `README.md` - Encoding library documentation
- `README_SERVICE.md` - Service application documentation
- `PLAN.md` - Implementation plan
- `SPEC.md` - Original specifications
- `IMPLEMENTATION_SUMMARY.md` - What was built

## Support

Check console logs first. Most issues are related to:
- Configuration (API keys, MongoDB URI)
- Wallet funding (need satoshis)
- Network connectivity

For detailed help, see README_SERVICE.md

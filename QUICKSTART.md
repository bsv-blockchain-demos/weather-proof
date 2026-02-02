# Weather Chain - Quick Start Guide

Get Weather Chain running locally for development.

## Prerequisites

- Node.js 18+ and npm
- MongoDB instance (local or Docker)
- BSV testnet wallet with satoshis
- Tempest weather API key (from weatherflow.com)

## Installation

```bash
# Install backend dependencies
npm install

# Install frontend dependencies
cd frontend && npm install && cd ..

# Build backend
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
API_PORT=3001
CORS_ORIGIN=http://localhost:5173
```

## Start MongoDB

Using Docker (recommended):

```bash
make dev
```

Or start MongoDB locally:

```bash
mongod --dbpath /path/to/data
```

## Setup Funding Basket

Create the initial funding outputs (one-time):

```bash
npm run setup
```

This creates 1000 hash puzzle UTXOs for transaction funding.

## Running the Application

### Start Backend

```bash
npm start
# Or for development with auto-reload:
npm run dev
```

The backend will:
- Poll Tempest API every 5 minutes
- Store weather data in MongoDB queue
- Process queue into blockchain transactions
- Monitor funding basket and auto-refill
- Serve REST API on port 3001

### Start Frontend

In a new terminal:

```bash
cd frontend
npm run dev
```

The frontend will be available at http://localhost:5173

## Accessing the Application

- **Frontend**: http://localhost:5173
- **API**: http://localhost:3001/api
- **Health Check**: http://localhost:3001/api/health

## Verify It's Working

### 1. Check Backend Logs

```
[INFO] Connected to MongoDB successfully
[INFO] Wallet initialized
[INFO] Starting weather data polling (interval: 300s)
[INFO] Starting record processor (interval: 3s)
[STATUS] Queue: 5 pending, 0 processing, 10 completed, 0 failed
```

### 2. Check Frontend

Open http://localhost:5173 and you should see:
- Weather records grid (after first poll completes)
- Status filter dropdown
- Pagination controls

### 3. Check Database

```bash
mongosh mongodb://localhost:27017/weather-chain
> db.weatherrecords.find({status: 'completed'}).limit(5)
```

### 4. Check API

```bash
curl http://localhost:3001/api/health
curl http://localhost:3001/api/weather
```

## Frontend Features

### Weather List Page (/)
- Grid of weather records with temperature, conditions, humidity, wind
- Filter by status: All, Pending, Processing, Completed, Failed
- Pagination with 12 records per page
- Click any card to view details

### Weather Detail Page (/weather/:id)
- Full weather data (33 fields in 6 categories)
- Blockchain information (txid, output index)
- Verification badge showing confirmation status
- "Verify on Blockchain" button for confirmed transactions
- Link to WhatsOnChain block explorer

### Verification States
- **pending/processing**: Record not yet on blockchain
- **Pending Confirmation**: Transaction broadcast, waiting for mining
- **On Chain**: Transaction confirmed, verification available
- **Verified**: User clicked verify, proof validated

## Testing

```bash
# Run backend tests
npm test

# Run with coverage
npm run test:coverage

# Lint code
npm run lint

# Format code
npm run format
```

## Troubleshooting

### "Insufficient funds to create funding outputs"

Your wallet needs satoshis. Fund the wallet address and retry:

```bash
npm run setup
```

### "TEMPEST_API_KEY is required"

Add your Tempest API key to `.env` file.

### "Failed to connect to MongoDB"

Ensure MongoDB is running:

```bash
# If using Docker
docker ps | grep mongo
make dev

# If local
mongod --dbpath /path/to/data
```

### Frontend shows "Failed to load weather records"

1. Check backend is running on port 3001
2. Check CORS_ORIGIN in .env matches frontend URL
3. Check browser console for errors

### No weather data appearing

1. Wait 5 minutes for first poll (or check logs)
2. Verify TEMPEST_API_KEY is valid
3. Check MongoDB has records: `db.weatherrecords.countDocuments()`

### Verification button not showing

The "Verify on Blockchain" button only appears for transactions that are confirmed on-chain (have a merkle path). New transactions may take a few minutes to be mined.

## Development Workflow

1. Start MongoDB: `make dev`
2. Start backend: `npm run dev` (auto-reloads on changes)
3. Start frontend: `cd frontend && npm run dev` (hot module replacement)
4. Make changes and see results immediately

## Next Steps

1. Monitor first transactions on WhatsOnChain
2. Click "Verify on Blockchain" to test SPV verification
3. Adjust POLL_RATE for more/less frequent data
4. Set up production deployment with Docker

## Documentation

- `README.md` - Full documentation
- `DOCKER_QUICKSTART.md` - Docker deployment
- `ENCODING.md` - Bitcoin Script encoding details
- `CONTRIBUTING.md` - How to contribute

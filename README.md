# Weather Chain

A full-stack BSV blockchain application that sources live weather data from the Tempest API, stores it immutably on the blockchain as OP_RETURN outputs, and provides a React frontend for browsing and verifying weather records.

## Features

### Backend Service
- **Weather Data Encoding**: Lossless encoding of 33 weather fields using Bitcoin Script
- **Tempest API Integration**: Automatic polling of weather stations (configurable interval)
- **Funding Basket**: Hash puzzle UTXOs for transaction fees with auto-refill
- **MongoDB Queue**: Async processing with failure recovery and retry logic
- **REST API**: Endpoints for querying weather records and blockchain proofs
- **Notifications**: Console logging with optional Twilio SMS alerts

### Frontend Application
- **Weather Dashboard**: Browse paginated weather records with status filtering
- **Record Details**: View all 33 weather data fields for each record
- **Blockchain Verification**: Client-side SPV verification using BEEF proofs
- **Confirmation Status**: Visual indicators for on-chain confirmation state
- **Responsive Design**: Mobile-first UI with TailwindCSS

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         Frontend (React)                         │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────────┐ │
│  │ Weather List │  │Weather Detail│  │ Blockchain Verification│ │
│  └──────────────┘  └──────────────┘  └────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
                              │
                         REST API
                              │
┌─────────────────────────────────────────────────────────────────┐
│                      Backend (Express.js)                        │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌───────────┐ │
│  │   Poller   │  │  Processor │  │   Monitor  │  │    API    │ │
│  │ (Tempest)  │  │  (Queue)   │  │ (Funding)  │  │ (Express) │ │
│  └────────────┘  └────────────┘  └────────────┘  └───────────┘ │
└─────────────────────────────────────────────────────────────────┘
        │                 │                              │
        ▼                 ▼                              ▼
┌──────────────┐  ┌──────────────┐              ┌──────────────┐
│ Tempest API  │  │   MongoDB    │              │ BSV Network  │
└──────────────┘  └──────────────┘              └──────────────┘
```

## Quick Start

### Docker (Recommended)

```bash
# Configure environment
cp .env.docker .env
# Edit .env and set TEMPEST_API_KEY

# Build and start all services
make build
make up

# Create funding basket (one-time)
make setup

# Access the application
# Frontend: http://localhost:5173
# API: http://localhost:3001/api
```

### Local Development

```bash
# Install dependencies
npm install
cd frontend && npm install && cd ..

# Configure environment
cp .env.example .env
# Edit .env with your settings

# Start MongoDB (via Docker or local install)
make dev  # or: mongod

# Create funding basket
npm run setup

# Start backend
npm run dev

# Start frontend (in another terminal)
cd frontend && npm run dev
```

## Configuration

Copy `.env.example` to `.env` and configure:

### Required
| Variable | Description |
|----------|-------------|
| `TEMPEST_API_KEY` | Your Tempest weather API key |
| `MONGO_URI` | MongoDB connection string |

### Optional (with defaults)
| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_PRIVATE_KEY` | (provided) | BSV wallet private key (hex) |
| `WALLET_STORAGE_URL` | `https://store-us-1.bsvb.tech` | Wallet storage provider |
| `BSV_NETWORK` | `test` | Network: `test` or `main` |
| `POLL_RATE` | `300` | Seconds between weather API polls |
| `FUNDING_OUTPUT_AMOUNT` | `1000` | Satoshis per funding output |
| `FUNDING_BASKET_MIN` | `200` | Minimum outputs before refill |
| `FUNDING_BATCH_SIZE` | `1000` | Outputs created per refill |
| `WEATHER_OUTPUTS_PER_TX` | `100` | Weather outputs per transaction |
| `API_PORT` | `3001` | Backend API port |
| `CORS_ORIGIN` | `http://localhost:5173` | Frontend origin for CORS |

### Frontend Environment
| Variable | Default | Description |
|----------|---------|-------------|
| `VITE_API_URL` | `http://localhost:3001` | Backend API URL |
| `VITE_BSV_NETWORK` | `test` | BSV network for block explorer links |

## API Endpoints

### Health Check
- `GET /api/health` - Service health status

### Weather Records
- `GET /api/weather` - List records with pagination
  - Query: `page`, `limit`, `status`, `stationId`
- `GET /api/weather/:id` - Get single record by ID

### Blockchain Proofs
- `GET /api/proof/:txid` - Get BEEF proof for verification

## How It Works

### 1. Weather Data Collection
The poller fetches current conditions from all configured Tempest weather stations every 5 minutes (configurable). Each record contains 33 fields including temperature, humidity, wind, pressure, precipitation, and lightning data.

### 2. Blockchain Storage
Weather data is encoded into Bitcoin Script and stored as OP_RETURN outputs:
```
OP_FALSE OP_RETURN <version> <field1> <field2> ... <field33>
```

Data types:
- **Integers**: Native Bitcoin Script encoding
- **Floats**: Fixed-point with 10^6 scale (6 decimal precision)
- **Strings**: UTF-8 bytes
- **Booleans**: 0 or 1

### 3. Transaction Funding
Hash puzzle outputs provide transaction funding:
- Created during setup with SHA256 puzzle locking scripts
- Preimages stored in wallet's `customInstructions`
- Auto-refill when basket drops below threshold

### 4. Blockchain Verification
The frontend performs client-side SPV verification:
- Fetches BEEF (Background Evaluation Extended Format) proof from API
- Verifies merkle path against block headers via WhatsOnChain
- Displays confirmation status and block height

### 5. Confirmation Status
Records show different states:
- **Pending Confirmation**: Transaction broadcast but not yet mined
- **On Chain**: Transaction confirmed with merkle proof available
- **Verified**: User-triggered verification completed successfully

## Frontend Features

### Weather List
- Paginated grid of weather records (12 per page)
- Filter by status: All, Pending, Processing, Completed, Failed
- Quick view of temperature, conditions, humidity, and wind
- Status badges showing processing and confirmation state

### Weather Detail
- Complete weather data across 6 categories:
  - Temperature & humidity
  - Atmospheric pressure
  - Wind conditions
  - Solar & UV data
  - Precipitation
  - Lightning activity
- Blockchain record information (txid, output index, block height)
- One-click blockchain verification for confirmed transactions
- Links to block explorer (WhatsOnChain)

## Project Structure

```
weather-chain/
├── src/                    # Backend source
│   ├── api/               # Express routes
│   ├── config/            # Environment configuration
│   ├── db/                # MongoDB models
│   ├── format/            # Bitcoin Script encoder/decoder
│   ├── notification/      # Alert services
│   ├── scripts/           # Setup scripts
│   ├── service/           # Core services
│   └── app.ts             # Application entry point
├── frontend/              # React frontend
│   ├── src/
│   │   ├── components/    # React components
│   │   ├── hooks/         # Custom React hooks
│   │   ├── services/      # API client & verification
│   │   └── types/         # TypeScript types
│   └── package.json
├── docker-compose.yaml    # Docker orchestration
├── Dockerfile             # Backend container
└── Makefile              # Development commands
```

## Testing

```bash
# Run backend tests
npm test

# Run with coverage
npm run test:coverage

# Run in watch mode
npm run test:watch
```

## Docker Commands

```bash
make build      # Build all images
make up         # Start all services
make down       # Stop all services
make setup      # Create funding basket
make logs       # View logs
make status     # Check service status
make mongo-shell # Open MongoDB shell
make dev        # Start MongoDB only (for local dev)
make clean      # Remove volumes and images
```

## Documentation

- `QUICKSTART.md` - Local development quick start
- `DOCKER_QUICKSTART.md` - Docker deployment guide
- `DOCKER.md` - Production deployment details
- `ENCODING.md` - Bitcoin Script encoding specification
- `CONTRIBUTING.md` - Contribution guidelines

## License

Open BSV License

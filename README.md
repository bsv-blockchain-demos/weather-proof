# Weather Proof

A full-stack BSV blockchain application that sources live weather data from the Tempest API, stores it immutably on the blockchain as OP_RETURN outputs, and provides a React frontend for browsing and verifying weather records.

## Features

### Backend Service
- **Weather Data Encoding**: A fixed schema for 33 weather fields using Bitcoin Script, with floating-point values rounded to six decimal places
- **Tempest API Integration**: Automatic polling of weather stations (configurable interval)
- **Funding Basket**: Hash puzzle UTXOs for transaction fees with auto-refill
- **MongoDB Queue**: Async processing with failure recovery and retry logic
- **REST API**: Endpoints for querying weather records and blockchain proofs
- **Notifications**: Console logging; a Twilio adapter exists but is not selected by the application entry point

### Frontend Application
- **Weather Dashboard**: Browse paginated weather records with status filtering
- **Record Details**: View all 33 weather data fields for each record
- **Confirmation Checks**: Backend WhatsOnChain lookups with persisted block heights; a separate BEEF proof endpoint and verification helper are also included
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

Use Node.js 22 and npm, a Tempest API key, MongoDB, and a dedicated funded BSV wallet with a compatible wallet storage service. The verification endpoint persists results inside a MongoDB transaction, so use a replica set or sharded cluster for that feature. Go 1.26.3+ is only needed for the separate encoder package in `internal/weather/`.

### Local development

```sh
git clone https://github.com/bsv-blockchain-demos/weather-proof.git
cd weather-proof
npm install
npm --prefix frontend install
cp .env.example .env
```

Edit `.env` before running the service. Replace the bundled development private key with your own, select the network explicitly, configure its wallet storage URL, and supply `TEMPEST_API_KEY` and `MONGO_URI`. Fund the wallet through its storage provider before creating the funding basket.

```sh
npm run setup
npm run dev
```

`setup` creates funding outputs and spends wallet funds. The backend also checks and refills its funding basket during normal operation.

In a second terminal, from the repository root:

```sh
npm --prefix frontend run dev
```

Open [localhost:5173](http://localhost:5173), then select the explorer to browse stations. Vite proxies `/api` to the backend at port 3001. Set `VITE_BSV_NETWORK` in `frontend/.env` to match `BSV_NETWORK`.

### Docker status

The [Compose file](docker-compose.yaml) includes MongoDB, the backend, the frontend and an optional setup service. It needs configuration changes before it is a complete deployment:

- The frontend reads its `VITE_` variables at build time, but Compose currently supplies them at runtime. Its default relative API requests have no reverse proxy in the supplied static server.
- The backend defaults to `test`, while some other service defaults use `main`. Set the network consistently in both the backend and frontend build.
- MongoDB is configured as a standalone instance, which does not support the transactions used by `/api/verify`.
- `make up` starts MongoDB and the backend only. It does not start the frontend.

Use the local workflow above for development. The older Docker guides describe intended workflows and should be read alongside these limitations.

## Configuration

Copy `.env.example` to `.env` and configure:

### Required
| Variable | Description |
|----------|-------------|
| `TEMPEST_API_KEY` | Your Tempest weather API key |
| `SERVER_PRIVATE_KEY` | Your own server wallet private key in hex. Do not fund the bundled development key. |
| `MONGO_URI` | MongoDB connection string |

### Optional (with defaults)
| Variable | Default | Description |
|----------|---------|-------------|
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
| `VITE_API_URL` | Empty, same origin | Browser-reachable backend origin. Vite proxies `/api` locally when this is unset. |
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
- `POST /api/verify` - Look up confirmation status for `{ txids: string[] }` and persist confirmed block heights

### Stations and live statistics
- `GET /api/stations` - Paginated station dashboard
- `GET /api/stations/:stationId` - Station details
- `GET /api/events` - Server-sent events for live statistics

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

### 4. Confirmation checks and proof support

The active frontend calls `POST /api/verify`. The backend looks up transaction confirmation status through WhatsOnChain and stores confirmed block heights in MongoDB. The interface's verification button uses this same lookup.

A separate BEEF endpoint and client-side `verifyWeatherProof` helper exist, but the current view does not call that helper. The active UI therefore does not independently perform SPV proof verification or compare a displayed observation against decoded on-chain data.

### 5. Confirmation status

Records show processing status and whether a block height has been returned. A completed queue item means the transaction was processed; it does not by itself mean the transaction is mined. The interface uses the confirmation lookup to update that state.

## Frontend Features

### Explorer
- `/` presents the project and live statistics
- `/explorer` lists stations with search, sorting and pagination
- `/station/:stationId` shows a station's weather records and status filters
- `/weather/:id` shows a single record and its proof controls

Confirmation lookups establish whether a transaction has been mined. They do not establish whether the original weather observation was accurate.

### Weather Detail
- Complete weather data across 6 categories:
  - Temperature & humidity
  - Atmospheric pressure
  - Wind conditions
  - Solar & UV data
  - Precipitation
  - Lightning activity
- Blockchain record information (txid, output index, block height)
- One-click confirmation lookup through the backend
- Links to block explorer (WhatsOnChain)

## Project Structure

```
weather-proof/
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

The repository also contains a Go implementation of the encoding format, separate from the running TypeScript service:

```sh
go test ./...
go build ./...
```

## Docker Commands

```bash
make build      # Build all images
make up         # Start MongoDB and backend only
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

## Licence

**Open BSV Licence v6.** See [LICENSE.txt](LICENSE.txt) for the full terms. The licence applies to this project's original code and documentation and restricts use to the BSV blockchain defined in the licence. Third-party code, assets and referenced standards retain their respective terms.

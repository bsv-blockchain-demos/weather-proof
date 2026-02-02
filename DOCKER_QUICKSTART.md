# Weather Chain - Docker Quick Start

Get Weather Chain running in Docker with the full stack (MongoDB, backend, frontend).

## Prerequisites

- Docker installed
- Docker Compose V2 installed
- Tempest API key (from weatherflow.com)

## Quick Setup (5 Steps)

### 1. Configure Environment

```bash
cd weather-chain
cp .env.docker .env
```

Edit `.env` and set your `TEMPEST_API_KEY`:

```bash
TEMPEST_API_KEY=your_actual_key_here
```

### 2. Build Images

```bash
make build
```

Or using docker-compose directly:

```bash
docker-compose build
```

### 3. Start Services

```bash
make up
```

Or:

```bash
docker-compose up -d
```

This starts:
- **MongoDB** on port 27017
- **Backend API** on port 3001
- **Frontend** on port 5173

### 4. Create Funding Basket

```bash
make setup
```

Or:

```bash
docker-compose --profile setup run --rm setup
```

This creates 1000 hash puzzle funding outputs (one-time setup).

### 5. Access the Application

- **Frontend**: http://localhost:5173
- **API**: http://localhost:3001/api
- **API Health**: http://localhost:3001/api/health

## Verify It's Working

### Check Services

```bash
make status
# or: docker-compose ps
```

Expected output:
```
NAME                      STATUS              PORTS
weather-chain-app         Up (healthy)        0.0.0.0:3001->3001/tcp
weather-chain-frontend    Up                  0.0.0.0:5173->80/tcp
weather-chain-mongodb     Up (healthy)        0.0.0.0:27017->27017/tcp
```

### Check Logs

```bash
make logs
# or: docker-compose logs -f
```

Expected backend output:
```
[INFO] Connected to MongoDB successfully
[INFO] Wallet initialized
[INFO] Starting weather data polling (interval: 300s)
[INFO] Starting record processor (interval: 3s)
[STATUS] Queue: 5 pending, 0 processing, 10 completed, 0 failed
```

### Check Frontend

Open http://localhost:5173 in your browser. You should see:
- Weather records grid (after first poll at ~5 minutes)
- Status filter dropdown
- Pagination controls

### Check Database

```bash
make mongo-shell
> db.weatherrecords.countDocuments({})
> db.weatherrecords.find({status: 'completed'}).limit(3)
```

## Common Commands

```bash
# Build all images
make build

# Start all services
make up

# Stop all services
make down

# View logs (follow mode)
make logs

# Check service status
make status

# Restart a service
docker-compose restart app
docker-compose restart frontend

# Open MongoDB shell
make mongo-shell

# Run setup (create funding outputs)
make setup

# Clean up everything (WARNING: deletes data)
make clean
```

## Service Details

### MongoDB (weather-chain-mongodb)
- Image: mongo:8.0
- Port: 27017
- Auth: admin/password
- Data persisted in Docker volume

### Backend (weather-chain-app)
- Port: 3001
- Polls Tempest API every 5 minutes
- Processes queue every 3 seconds
- Monitors funding every 60 seconds

### Frontend (weather-chain-frontend)
- Port: 5173 (mapped from nginx port 80)
- React + TypeScript + Vite
- TailwindCSS styling
- Client-side blockchain verification

## Frontend Features

### Weather List
- Browse all weather records in a responsive grid
- Filter by status: All, Pending, Processing, Completed, Failed
- See temperature, conditions, humidity, wind at a glance
- Click any card to view full details

### Weather Detail
- View all 33 weather data fields
- See blockchain transaction info (txid, output index)
- Check confirmation status (Pending Confirmation vs On Chain)
- Verify on blockchain with one click (for confirmed transactions)
- Link to WhatsOnChain block explorer

### Verification States
- **Pending/Processing**: Record being processed
- **Pending Confirmation**: Transaction broadcast, awaiting mining
- **On Chain**: Transaction mined, verification available
- **Verified**: Proof validated against blockchain

## Troubleshooting

### Frontend Not Loading

**Symptom**: Browser shows connection refused

**Fix**:
```bash
# Check frontend container is running
docker-compose ps frontend

# Check logs
docker-compose logs frontend

# Restart frontend
docker-compose restart frontend
```

### Backend Not Connecting to MongoDB

**Symptom**: Connection refused errors in logs

**Fix**:
```bash
# Check MongoDB is healthy
docker-compose ps mongodb

# Wait for health check to pass
docker-compose logs mongodb

# Restart both services
docker-compose restart mongodb
sleep 10
docker-compose restart app
```

### Missing API Key

**Symptom**: "TEMPEST_API_KEY is required"

**Fix**:
```bash
# Edit .env file
nano .env
# Add: TEMPEST_API_KEY=your_key

# Restart app
docker-compose restart app
```

### Insufficient Funds

**Symptom**: "Insufficient funds to create funding outputs"

**Fix**:
- Fund your BSV wallet with satoshis
- Retry setup: `make setup`

### No Weather Data in Frontend

**Symptom**: "No weather records found"

**Cause**: First poll hasn't completed yet (takes ~5 minutes)

**Fix**:
```bash
# Check backend logs for polling status
docker-compose logs app | grep -E "(poll|STATUS)"

# Wait for: [STATUS] Queue: X pending, 0 processing, Y completed, 0 failed
```

### Verification Button Not Showing

**Symptom**: Can't verify a completed record

**Cause**: Transaction not yet confirmed (no merkle path)

**Fix**: Wait a few minutes for the transaction to be mined, then refresh the page.

## Development Mode

Run MongoDB in Docker, but backend and frontend locally:

```bash
# Start only MongoDB
make dev

# In terminal 1: Start backend
npm run dev

# In terminal 2: Start frontend
cd frontend && npm run dev
```

This allows faster iteration with hot module replacement.

## Environment Variables

### Backend (.env)
```bash
TEMPEST_API_KEY=your_key          # Required
MONGO_URI=mongodb://...           # Set by docker-compose
SERVER_PRIVATE_KEY=...            # BSV wallet key
BSV_NETWORK=test                  # test or main
POLL_RATE=300                     # Seconds between polls
API_PORT=3001                     # API server port
CORS_ORIGIN=http://localhost:5173 # Frontend URL
```

### Frontend (set in docker-compose.yaml)
```bash
VITE_API_URL=http://app:3001      # Backend API URL (internal)
VITE_BSV_NETWORK=test             # For block explorer links
```

## Production Deployment

See `DOCKER.md` for:
- Security hardening
- Resource limits
- SSL/TLS configuration
- Logging and monitoring
- Backup procedures

## Summary

Weather Chain is now running with:
- MongoDB for data persistence
- Backend API processing weather data
- Frontend for browsing and verification
- All services orchestrated via Docker Compose

**Access Points**:
- Frontend: http://localhost:5173
- API: http://localhost:3001/api
- MongoDB: localhost:27017

**Quick Commands**:
- Start: `make up`
- Stop: `make down`
- Logs: `make logs`
- Setup: `make setup`

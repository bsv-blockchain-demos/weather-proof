# Weather Chain Frontend

React frontend for browsing and verifying weather data stored on the BSV blockchain.

## Features

- **Weather Dashboard**: Browse paginated weather records with filtering
- **Record Details**: View all 33 weather data fields
- **Blockchain Verification**: Client-side SPV verification using BEEF proofs
- **Confirmation Status**: Visual indicators for on-chain confirmation state
- **Responsive Design**: Mobile-first UI with TailwindCSS

## Tech Stack

- React 18 with TypeScript
- Vite for build tooling
- TanStack React Query for data fetching
- React Router for navigation
- TailwindCSS for styling
- @bsv/sdk for blockchain verification

## Development

### Prerequisites

- Node.js 18+
- Backend API running on port 3001

### Setup

```bash
# Install dependencies
npm install

# Start development server
npm run dev
```

The app will be available at http://localhost:5173

### Environment Variables

Create a `.env` file or set these variables:

```bash
VITE_API_URL=http://localhost:3001    # Backend API URL
VITE_BSV_NETWORK=test                  # 'test' or 'main' for explorer links
```

### Scripts

```bash
npm run dev      # Start development server with HMR
npm run build    # Build for production
npm run preview  # Preview production build
npm run lint     # Run ESLint
```

## Project Structure

```
src/
├── components/
│   ├── App.tsx              # Main app with routing
│   ├── WeatherList.tsx      # Paginated weather grid
│   ├── WeatherCard.tsx      # Individual weather card
│   ├── WeatherDetail.tsx    # Full record details
│   └── VerificationBadge.tsx # Status and verification UI
├── hooks/
│   ├── useWeather.ts        # Weather data fetching
│   └── useVerification.ts   # Blockchain verification
├── services/
│   ├── api.ts               # REST API client
│   └── verify.ts            # BEEF verification logic
├── types/
│   └── weather.ts           # TypeScript interfaces
└── main.tsx                 # Entry point
```

## Pages

### Weather List (/)

Displays a paginated grid of weather records.

**Features**:
- Filter by status (All, Pending, Processing, Completed, Failed)
- 12 records per page with pagination
- Each card shows: temperature, conditions, feels-like, humidity, wind
- Status badge indicating processing and confirmation state

### Weather Detail (/weather/:id)

Shows complete details for a single weather record.

**Weather Data Groups**:
- Temperature: air temp, feels like, dew point, wet bulb temperatures
- Atmosphere: humidity, pressure (station & sea level), air density
- Wind: speed, gust, direction (degrees and cardinal)
- Solar: radiation, UV index, brightness
- Precipitation: probability, accumulation, duration
- Lightning: strike counts, distance, timestamps

**Blockchain Info**:
- Transaction ID (linked to WhatsOnChain)
- Output index
- Block height (after verification)
- Confirmation status

**Verification**:
- Shows "Pending Confirmation" until transaction is mined
- "Verify on Blockchain" button appears when confirmed
- Verification validates merkle proof against chain headers

## Verification Flow

1. When viewing a completed record, the app fetches the BEEF proof from `/api/proof/:txid`
2. The BEEF is parsed to check if it contains a merkle path (indicates confirmation)
3. If confirmed, user can click "Verify on Blockchain"
4. Verification uses WhatsOnChain to validate the merkle proof
5. Success shows block height; failure shows error message

## API Integration

The frontend communicates with the backend API:

```typescript
// Fetch weather records with pagination
GET /api/weather?page=1&limit=12&status=completed

// Fetch single record
GET /api/weather/:id

// Fetch BEEF proof for verification
GET /api/proof/:txid
```

## Styling

Uses TailwindCSS with a clean, minimal design:

- White cards with subtle shadows
- Indigo accent color for interactive elements
- Status badges: yellow (pending), blue (processing), green (completed/verified), red (failed)
- Responsive grid: 1 column mobile, 2 columns tablet, 3 columns desktop

## Building for Production

```bash
npm run build
```

Output is in `dist/` directory. Deploy to any static hosting.

### Docker

The included Dockerfile builds and serves via nginx:

```bash
docker build -t weather-chain-frontend .
docker run -p 5173:80 weather-chain-frontend
```

## Related Documentation

- [Main README](../README.md) - Full project documentation
- [QUICKSTART](../QUICKSTART.md) - Local development setup
- [DOCKER_QUICKSTART](../DOCKER_QUICKSTART.md) - Docker deployment

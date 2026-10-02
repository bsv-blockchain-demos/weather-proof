# Weather Proof frontend

React and TypeScript interface for browsing weather stations, inspecting recorded observations and checking BSV transaction proofs. It uses Vite, React Query, React Router, Tailwind CSS and `@bsv/sdk`.

The [root README](../README.md) covers the backend, Tempest integration and wallet funding.

## Run locally

Use Node.js 22 and npm. Start the backend on port 3001, then run these commands from `frontend/`:

```sh
npm install
npm run dev
```

Open [localhost:5173](http://localhost:5173). Vite proxies `/api` to `http://localhost:3001`.

Optional configuration in `frontend/.env`:

| Variable | Purpose |
| --- | --- |
| `VITE_API_URL` | Browser-reachable backend origin. Unset means same-origin `/api` requests, using Vite's proxy during development. |
| `VITE_BSV_NETWORK` | `main` or `test`, matching the backend. Defaults to `test`; controls proof verification and explorer links. |

Vite embeds these values during the build. Setting them only on a running static-file container does not change an existing bundle.

## Pages

| Route | Purpose |
| --- | --- |
| `/` | Landing page and live statistics |
| `/explorer` | Searchable, sortable station dashboard |
| `/station/:stationId` | Station details and paginated weather records |
| `/weather/:id` | Observation fields, transaction details and proof verification |

## Verification

Station record lists call `POST /api/verify` to obtain confirmation status and block heights. The backend queries WhatsOnChain and persists confirmed heights in MongoDB.

The detail view's verification button calls the same confirmation endpoint. Although `src/services/verify.ts` contains a BEEF verification helper and the backend exposes `/api/proof/:txid`, the current UI does not call that helper. Its displayed verification state is a backend confirmation lookup, not an independent SPV check or a comparison of the displayed observation with on-chain fields. Confirmation also does not establish the accuracy of the sensor reading.

## Build and serve

```sh
npm run build
npm run preview
```

The build writes `dist/`. A deployment needs a browser-reachable API, matching CORS settings, and a fallback to `index.html` for client-side routes. For an explicit API origin, set `VITE_API_URL` before building. The preview server does not provide the development proxy.

The included Dockerfile serves assets with `serve` on port 3000. It accepts a `VITE_API_URL` build argument:

```sh
docker build --build-arg VITE_API_URL=http://localhost:3001 -t weather-proof-frontend .
docker run --rm -p 5173:3000 weather-proof-frontend
```

This example targets a backend reachable from the browser at localhost. A remote deployment needs its own API origin. The Dockerfile does not currently expose a `VITE_BSV_NETWORK` build argument; supply that value through Vite's build environment or an appropriate build configuration. `nginx.conf` is not used by this Dockerfile.

## Source map

- [`src/App.tsx`](src/App.tsx): routes and explorer layout
- [`src/components/`](src/components/): landing page, station dashboard and record views
- [`src/hooks/`](src/hooks/): queries, automatic confirmation checks and live statistics
- [`src/services/api.ts`](src/services/api.ts): backend requests
- [`src/services/verify.ts`](src/services/verify.ts): BEEF proof verification

## Checks

`npm run build` performs TypeScript compilation and bundling. No automated frontend test script is provided. The lint script exists, but there is no ESLint configuration in this directory or the repository root.

## Licence

**Open BSV Licence v6.** See [LICENSE.txt](../LICENSE.txt) for the full terms. The licence applies to this project's original code and documentation and restricts use to the BSV blockchain defined in the licence. Third-party code, assets and referenced standards retain their respective terms.

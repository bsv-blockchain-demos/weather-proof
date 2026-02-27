import { Router, Request, Response } from 'express';
import { Station } from '../../db/models/station';
import { GlobalStats } from '../../db/models/global-stats';

const router = Router();

interface StationListQuery {
  page?: string;
  limit?: string;
  search?: string;
}

/**
 * GET /api/stations
 *
 * Returns global dashboard stats (O(1) — reads GlobalStats singleton) and a
 * paginated, searchable list of stations (reads Station collection only).
 *
 * No WeatherRecord queries — safe at any collection size.
 */
router.get('/', async (req: Request<object, object, object, StationListQuery>, res: Response) => {
  try {
    const page = Math.max(1, parseInt(req.query.page ?? '1', 10));
    const limit = Math.min(200, Math.max(1, parseInt(req.query.limit ?? '50', 10)));
    const skip = (page - 1) * limit;
    const search = req.query.search?.trim();

    // Build station filter
    const filter: Record<string, unknown> = {};
    if (search) {
      const asNum = parseInt(search, 10);
      if (!isNaN(asNum)) {
        // Exact stationId match when query is numeric
        filter.stationId = asNum;
      } else {
        // Full-text search on name + location via text index (efficient at 90K+ docs)
        filter.$text = { $search: search };
      }
    }

    // Run all three reads in parallel — none touch WeatherRecord
    const [globalStats, stations, stationTotal] = await Promise.all([
      GlobalStats.findOne().lean(),
      Station.find(filter)
        .sort(search && !parseInt(search, 10) ? { score: { $meta: 'textScore' } } : { stationId: 1 })
        .skip(skip)
        .limit(limit)
        .lean(),
      Station.countDocuments(filter),
    ]);

    res.json({
      stats: {
        // Fall back to Station count if GlobalStats hasn't been seeded yet
        activeStations: globalStats?.activeStations ?? stationTotal,
        totalTx: globalStats?.totalTx ?? 0,
        lastRecordWrite: globalStats?.lastRecordWrite ?? null,
        totalDataPoints: globalStats?.totalDataPoints ?? 0,
      },
      stations: stations.map((s) => ({
        stationId: s.stationId,
        name: s.name || `Station ${s.stationId}`,
        location: s.location || '',
        status: s.isActive ? 'online' : 'offline',
        lastReading: s.lastReading ?? null,
        lastTemp: s.lastTemp ?? null,
        lastConditions: s.lastConditions || '',
        txRecords: s.txRecords,
        lastBlockHeight: s.lastBlockHeight ?? null,
      })),
      pagination: {
        page,
        limit,
        total: stationTotal,
        totalPages: Math.ceil(stationTotal / limit),
      },
    });
  } catch (error) {
    console.error('Error fetching stations:', error);
    res.status(500).json({ error: 'Failed to fetch stations' });
  }
});

/**
 * GET /api/stations/:stationId
 *
 * Single station metadata + summary. Reads Station doc only.
 */
router.get('/:stationId', async (req: Request<{ stationId: string }>, res: Response) => {
  try {
    const stationId = parseInt(req.params.stationId, 10);

    if (isNaN(stationId)) {
      res.status(400).json({ error: 'Invalid station ID' });
      return;
    }

    const station = await Station.findOne({ stationId }).lean();

    if (!station) {
      res.status(404).json({ error: 'Station not found' });
      return;
    }

    res.json({
      stationId: station.stationId,
      name: station.name || `Station ${stationId}`,
      location: station.location || '',
      status: station.isActive ? 'online' : 'offline',
      lastReading: station.lastReading ?? null,
      lastTemp: station.lastTemp ?? null,
      lastConditions: station.lastConditions || '',
      txRecords: station.txRecords,
      lastBlockHeight: station.lastBlockHeight ?? null,
    });
  } catch (error) {
    console.error('Error fetching station:', error);
    res.status(500).json({ error: 'Failed to fetch station' });
  }
});

export default router;

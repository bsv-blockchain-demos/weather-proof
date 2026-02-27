import { Router, Request, Response } from 'express';
import { WeatherRecord, RecordStatus } from '../../db/models/weather-record';

const router = Router();

interface WeatherListQuery {
  page?: string;
  limit?: string;
  status?: RecordStatus;
  stationId?: string;
}

/**
 * GET /api/weather
 * List weather records with pagination and filtering
 */
router.get('/', async (req: Request<object, object, object, WeatherListQuery>, res: Response) => {
  try {
    const page = Math.max(1, parseInt(req.query.page ?? '1', 10));
    const limit = Math.min(100, Math.max(1, parseInt(req.query.limit ?? '20', 10)));
    const skip = (page - 1) * limit;

    // Build filter
    const filter: Record<string, unknown> = {};
    if (req.query.status) {
      filter.status = req.query.status;
    }
    if (req.query.stationId) {
      filter.stationId = parseInt(req.query.stationId, 10);
    }

    // Execute queries in parallel
    const [records, total] = await Promise.all([
      WeatherRecord.find(filter)
        .sort({ createdAt: -1 })
        .skip(skip)
        .limit(limit)
        .lean(),
      WeatherRecord.countDocuments(filter),
    ]);

    // Transform records to API response format
    const items = records.map((record) => ({
      id: record._id.toString(),
      stationId: record.stationId,
      timestamp: record.timestamp.toISOString(),
      data: record.data,
      blockchain: {
        txid: record.txid ?? null,
        outputIndex: record.outputIndex ?? null,
        blockHeight: record.blockHeight ?? null,
      },
      status: record.status,
      createdAt: record.createdAt.toISOString(),
      processedAt: record.processedAt?.toISOString() ?? null,
    }));

    res.json({
      items,
      pagination: {
        page,
        limit,
        total,
        totalPages: Math.ceil(total / limit),
      },
    });
  } catch (error) {
    console.error('Error fetching weather records:', error);
    res.status(500).json({ error: 'Failed to fetch weather records' });
  }
});

/**
 * GET /api/weather/:id
 * Get a single weather record by MongoDB ID
 */
router.get('/:id', async (req: Request<{ id: string }>, res: Response) => {
  try {
    const record = await WeatherRecord.findById(req.params.id).lean();

    if (!record) {
      res.status(404).json({ error: 'Weather record not found' });
      return;
    }

    res.json({
      id: record._id.toString(),
      stationId: record.stationId,
      timestamp: record.timestamp.toISOString(),
      data: record.data,
      blockchain: {
        txid: record.txid ?? null,
        outputIndex: record.outputIndex ?? null,
        blockHeight: record.blockHeight ?? null,
      },
      status: record.status,
      createdAt: record.createdAt.toISOString(),
      processedAt: record.processedAt?.toISOString() ?? null,
      error: record.error ?? null,
    });
  } catch (error) {
    console.error('Error fetching weather record:', error);
    res.status(500).json({ error: 'Failed to fetch weather record' });
  }
});

export default router;

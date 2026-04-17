import express, { Application, Request, Response, NextFunction } from 'express';
import cors from 'cors';
import rateLimit from 'express-rate-limit';
import { config } from '../config/env';
import weatherRoutes from './routes/weather';
import proofRoutes from './routes/proof';
import verifyRoutes from './routes/verify';
import stationsRoutes from './routes/stations';
import eventsRoutes from './routes/events';

/**
 * Create and configure the Express application
 */
export function createApp(): Application {
  const app = express();

  // Middleware
  app.use(cors({
    origin: config.CORS_ORIGIN,
    credentials: true,
  }));
  app.use(express.json());

  // Rate limiting — 100 requests per minute per IP for general API
  const apiLimiter = rateLimit({
    windowMs: 60 * 1000,
    max: 100,
    standardHeaders: true,
    legacyHeaders: false,
    message: { error: 'Too many requests, please try again later' },
  });
  app.use('/api', apiLimiter);

  // Stricter limit for SSE connections — 10 per minute per IP
  const sseLimiter = rateLimit({
    windowMs: 60 * 1000,
    max: 10,
    standardHeaders: true,
    legacyHeaders: false,
    message: { error: 'Too many SSE connections, please try again later' },
  });
  app.use('/api/events', sseLimiter);

  // Health check endpoint
  app.get('/api/health', (_req: Request, res: Response) => {
    res.json({ status: 'ok', timestamp: new Date().toISOString() });
  });

  // Mount routes
  app.use('/api/weather', weatherRoutes);
  app.use('/api/proof', proofRoutes);
  app.use('/api/verify', verifyRoutes);
  app.use('/api/stations', stationsRoutes);
  app.use('/api/events', eventsRoutes);

  // Error handling middleware
  app.use((err: Error, _req: Request, res: Response, _next: NextFunction) => {
    console.error('API Error:', err);
    res.status(500).json({
      error: 'Internal server error',
      message: err.message,
    });
  });

  return app;
}

/**
 * Start the API server
 */
export function startApiServer(): Promise<ReturnType<Application['listen']>> {
  return new Promise((resolve) => {
    const app = createApp();
    const server = app.listen(config.API_PORT, () => {
      console.log(`✓ API server listening on port ${config.API_PORT}`);
      resolve(server);
    });
  });
}

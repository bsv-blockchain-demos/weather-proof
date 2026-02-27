import mongoose, { Schema, Document } from 'mongoose';

/**
 * Global statistics — singleton document, never grows.
 * Counters are maintained via atomic $inc / $max during record processing
 * so the dashboard can read in O(1) regardless of collection size.
 */
export interface IGlobalStats extends Document {
  totalTx: number;
  totalDataPoints: number;
  lastRecordWrite: Date | null;
  activeStations: number;
  updatedAt: Date;
}

const GlobalStatsSchema = new Schema<IGlobalStats>(
  {
    totalTx: { type: Number, default: 0 },
    totalDataPoints: { type: Number, default: 0 },
    lastRecordWrite: { type: Date, default: null },
    activeStations: { type: Number, default: 0 },
  },
  { timestamps: true }
);

export const GlobalStats = mongoose.model<IGlobalStats>('GlobalStats', GlobalStatsSchema);

/**
 * Number of weather data fields per record.
 * Used to compute totalDataPoints = totalTx * DATA_FIELDS_PER_RECORD.
 */
export const DATA_FIELDS_PER_RECORD = 33;

/**
 * Atomically increment global stats after a batch of records is confirmed.
 * Returns the updated document so callers can broadcast the new values.
 *
 * @param recordCount   Number of records successfully written to chain
 * @param newStations   Number of brand-new station IDs first seen in this batch
 */
export async function incrementGlobalStats(
  recordCount: number,
  newStations: number
): Promise<IGlobalStats> {
  const doc = await GlobalStats.findOneAndUpdate(
    {},
    {
      $inc: {
        totalTx: recordCount,
        totalDataPoints: recordCount * DATA_FIELDS_PER_RECORD,
        ...(newStations > 0 ? { activeStations: newStations } : {}),
      },
      $max: { lastRecordWrite: new Date() },
    },
    { upsert: true, new: true }
  );
  return doc!;
}

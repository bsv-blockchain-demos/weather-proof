import mongoose, { Schema, Document } from 'mongoose';

/**
 * Station metadata + denormalized last-reading summary.
 *
 * Keeping the most recent reading here means the dashboard query
 * never touches the WeatherRecord collection, regardless of how many
 * transactions exist. The fields are updated atomically by the processor
 * after each successful batch.
 */
export interface IStation extends Document {
  stationId: number;
  name: string;
  location: string;
  latitude?: number;
  longitude?: number;
  isActive: boolean;
  // Denormalized counters — maintained by processor, never aggregated
  txRecords: number;
  lastReading: Date | null;
  lastTemp: number | null;
  lastConditions: string;
  lastBlockHeight: number | null;
  createdAt: Date;
  updatedAt: Date;
}

const StationSchema = new Schema<IStation>(
  {
    stationId: { type: Number, required: true, unique: true, index: true },
    name: { type: String, default: '' },
    location: { type: String, default: '' },
    latitude: { type: Number },
    longitude: { type: Number },
    isActive: { type: Boolean, default: true, index: true },
    // Denormalized summary — updated by processor on each batch
    txRecords: { type: Number, default: 0 },
    lastReading: { type: Date, default: null },
    lastTemp: { type: Number, default: null },
    lastConditions: { type: String, default: '' },
    lastBlockHeight: { type: Number, default: null },
  },
  { timestamps: true }
);

// Text index for efficient name/location search at 90K+ stations.
// $regex with $options:'i' would be a collection scan; $text uses this index.
StationSchema.index({ name: 'text', location: 'text' });

export const Station = mongoose.model<IStation>('Station', StationSchema);

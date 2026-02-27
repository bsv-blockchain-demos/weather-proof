import mongoose, { Schema, Document } from 'mongoose';
import { WeatherData } from '../../format/types';

/**
 * Weather record status enum
 */
export type RecordStatus = 'pending' | 'processing' | 'completed' | 'failed';

/**
 * Weather record interface for database
 */
export interface IWeatherRecord extends Document {
  stationId: number;
  timestamp: Date;
  data: WeatherData;
  status: RecordStatus;
  txid?: string;
  outputIndex?: number;
  blockHeight?: number;
  error?: string;
  createdAt: Date;
  processedAt?: Date;
}

/**
 * Weather record schema
 */
const WeatherRecordSchema = new Schema<IWeatherRecord>({
  stationId: {
    type: Number,
    required: true,
    index: true,
  },
  timestamp: {
    type: Date,
    required: true,
    index: true,
  },
  data: {
    type: Schema.Types.Mixed,
    required: true,
  },
  status: {
    type: String,
    enum: ['pending', 'processing', 'completed', 'failed'],
    default: 'pending',
    index: true,
  },
  txid: {
    type: String,
    index: true,
  },
  outputIndex: {
    type: Number,
  },
  blockHeight: {
    type: Number,
    index: true,
  },
  error: {
    type: String,
  },
  createdAt: {
    type: Date,
    default: Date.now,
    index: true,
  },
  processedAt: {
    type: Date,
  },
});

// Compound indexes for efficient queries
WeatherRecordSchema.index({ status: 1, createdAt: 1 });
WeatherRecordSchema.index({ stationId: 1, timestamp: 1 });
// Supports the station records page: find({stationId}).sort({createdAt:-1}).skip().limit()
WeatherRecordSchema.index({ stationId: 1, createdAt: -1 });

/**
 * Weather record model
 */
export const WeatherRecord = mongoose.model<IWeatherRecord>('WeatherRecord', WeatherRecordSchema);

import mongoose from 'mongoose';
import { config } from '../config/env';

/**
 * Connect to MongoDB database
 */
export async function connectMongo(): Promise<void> {
  try {
    await mongoose.connect(config.MONGO_URI);
    console.log('Connected to MongoDB successfully');
  } catch (error) {
    console.error('Failed to connect to MongoDB:', error);
    throw error;
  }
}

/**
 * Disconnect from MongoDB database
 */
export async function disconnectMongo(): Promise<void> {
  try {
    await mongoose.disconnect();
    console.log('Disconnected from MongoDB');
  } catch (error) {
    console.error('Failed to disconnect from MongoDB:', error);
    throw error;
  }
}

/**
 * Check MongoDB connection status
 */
export function isConnected(): boolean {
  return mongoose.connection.readyState === 1;
}

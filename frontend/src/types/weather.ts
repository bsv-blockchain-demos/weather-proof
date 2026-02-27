/**
 * Weather data structure (mirrors backend format/types.ts)
 */
export interface WeatherData {
  air_density: number;
  air_temperature: number;
  brightness: number;
  conditions: string;
  delta_t: number;
  dew_point: number;
  feels_like: number;
  icon: string;
  is_precip_local_day_rain_check: boolean;
  is_precip_local_yesterday_rain_check: boolean;
  lightning_strike_count_last_1hr: number;
  lightning_strike_count_last_3hr: number;
  lightning_strike_last_distance: number;
  lightning_strike_last_distance_msg: string;
  lightning_strike_last_epoch: number;
  precip_accum_local_day: number;
  precip_accum_local_yesterday: number;
  precip_minutes_local_day: number;
  precip_minutes_local_yesterday: number;
  precip_probability: number;
  pressure_trend: string;
  relative_humidity: number;
  sea_level_pressure: number;
  solar_radiation: number;
  station_pressure: number;
  time: number;
  uv: number;
  wet_bulb_globe_temperature: number;
  wet_bulb_temperature: number;
  wind_avg: number;
  wind_direction: number;
  wind_direction_cardinal: string;
  wind_gust: number;
}

/**
 * Record status from API
 */
export type RecordStatus = 'pending' | 'processing' | 'completed' | 'failed';

/**
 * Weather record from API
 */
export interface WeatherRecord {
  id: string;
  stationId: number;
  timestamp: string;
  data: WeatherData;
  blockchain: {
    txid: string | null;
    outputIndex: number | null;
    blockHeight: number | null;
  };
  status: RecordStatus;
  createdAt: string;
  processedAt: string | null;
  error?: string | null;
}

/**
 * Paginated response from API
 */
export interface PaginatedResponse<T> {
  items: T[];
  pagination: {
    page: number;
    limit: number;
    total: number;
    totalPages: number;
  };
}

/**
 * BEEF proof response from API
 */
export interface BeefProof {
  txid: string;
  beef: string;
}

/**
 * Station summary for the dashboard
 */
export interface StationSummary {
  stationId: number;
  name: string;
  location: string;
  status: 'online' | 'offline';
  lastReading: string | null;
  lastTemp: number | null;
  lastConditions: string;
  txRecords: number;
  lastBlockHeight: number | null;
}

/**
 * Global dashboard stats
 */
export interface DashboardStats {
  activeStations: number;
  totalTx: number;
  lastRecordWrite: string | null;
  totalDataPoints: number;
}

/**
 * Dashboard API response
 */
export interface DashboardResponse {
  stats: DashboardStats;
  stations: StationSummary[];
  pagination: {
    page: number;
    limit: number;
    total: number;
    totalPages: number;
  };
}

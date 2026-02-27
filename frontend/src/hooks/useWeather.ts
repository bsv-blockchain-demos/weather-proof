import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { fetchWeatherRecords, fetchWeatherRecord, fetchDashboard, fetchStation } from '../services/api';
import type { RecordStatus } from '../types/weather';

/**
 * Hook for fetching paginated weather records.
 * keepPreviousData keeps the current table visible while the next page loads,
 * showing a dim overlay via isFetching instead of a blank/spinner.
 */
export function useWeatherRecords(params: {
  page?: number;
  limit?: number;
  status?: RecordStatus;
  stationId?: number;
} = {}) {
  return useQuery({
    queryKey: ['weather', 'list', params],
    queryFn: () => fetchWeatherRecords(params),
    placeholderData: keepPreviousData,
  });
}

/**
 * Hook for fetching a single weather record.
 * Long staleTime because records are essentially immutable once confirmed on-chain.
 */
export function useWeatherRecord(id: string | undefined) {
  return useQuery({
    queryKey: ['weather', 'detail', id],
    queryFn: () => fetchWeatherRecord(id!),
    enabled: !!id,
    staleTime: 5 * 60_000,
  });
}

/**
 * Hook for the dashboard: global stats + paginated station list.
 * keepPreviousData means the station table stays visible during page/search changes.
 * refetchInterval keeps live stats ticking every 30s.
 */
export function useDashboard(params: {
  page?: number;
  limit?: number;
  search?: string;
} = {}) {
  return useQuery({
    queryKey: ['dashboard', params],
    queryFn: () => fetchDashboard(params),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  });
}

/**
 * Hook for fetching a single station's metadata and summary.
 */
export function useStation(stationId: number | undefined) {
  return useQuery({
    queryKey: ['station', stationId],
    queryFn: () => fetchStation(stationId!),
    enabled: !!stationId,
  });
}

/**
 * Hook for fetching paginated records for a specific station.
 * keepPreviousData keeps the tx table visible while paginating.
 */
export function useStationRecords(stationId: number | undefined, params: {
  page?: number;
  limit?: number;
} = {}) {
  return useQuery({
    queryKey: ['weather', 'list', { stationId, ...params }],
    queryFn: () => fetchWeatherRecords({ stationId, ...params, limit: params.limit ?? 20 }),
    enabled: !!stationId,
    placeholderData: keepPreviousData,
  });
}

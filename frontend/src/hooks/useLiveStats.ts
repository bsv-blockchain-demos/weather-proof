import { useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import type { DashboardResponse, DashboardStats } from '../types/weather';

const API_BASE = import.meta.env.VITE_API_URL || '';

export type LiveStatus = 'connecting' | 'connected' | 'disconnected';

/**
 * Connects to the SSE stream at /api/events and keeps the React Query
 * dashboard cache in sync with live stats, with no polling or refetch.
 *
 * When a 'stats_update' event arrives, setQueriesData walks every cached
 * dashboard page (all page/search combinations) and replaces just the
 * stats slice, leaving the station list untouched.
 *
 * Returns the current connection status so the UI can show a live indicator.
 */
export function useLiveStats(): LiveStatus {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState<LiveStatus>('connecting');
  // Keep a stable ref so the effect closure always sees the latest queryClient
  const queryClientRef = useRef(queryClient);
  queryClientRef.current = queryClient;

  useEffect(() => {
    const es = new EventSource(`${API_BASE}/api/events`);

    es.addEventListener('stats_update', (e: MessageEvent) => {
      try {
        const stats: DashboardStats = JSON.parse(e.data);
        setStatus('connected');

        // Surgically update the stats slice of every cached dashboard query.
        // This covers all page numbers and search terms simultaneously without
        // triggering a network refetch.
        queryClientRef.current.setQueriesData<DashboardResponse>(
          { queryKey: ['dashboard'] },
          (old) => {
            if (!old) return old;
            return { ...old, stats };
          }
        );
      } catch {
        // Malformed event — ignore
      }
    });

    es.onopen = () => setStatus('connected');

    es.onerror = () => {
      setStatus('disconnected');
      // EventSource retries automatically — status will flip back to 'connected'
      // once the connection re-establishes, no manual intervention needed.
    };

    return () => {
      es.close();
      setStatus('disconnected');
    };
  }, []); // Empty deps: one connection per Dashboard mount, cleaned up on unmount

  return status;
}

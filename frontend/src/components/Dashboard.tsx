import { useState, useCallback, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { useDashboard } from '../hooks/useWeather';
import { useLiveStats } from '../hooks/useLiveStats';
import { fetchDashboard, fetchStation, fetchWeatherRecords } from '../services/api';
import type { StationSummary } from '../types/weather';

const PAGE_SIZE = 50;
const RECORDS_PAGE_SIZE = 20;

function formatDateTime(ts: string | null): string {
  if (!ts) return '—';
  return new Date(ts).toLocaleString('en-GB', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

function formatLargeNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`;
  return n.toLocaleString();
}

interface StatCardProps {
  label: string;
  value: string;
}

function StatCard({ label, value }: StatCardProps) {
  return (
    <div className="bg-gray-800 border border-gray-700 rounded-lg px-5 py-4 flex flex-col gap-1 min-w-0">
      <span className="text-xs text-gray-400 uppercase tracking-wide">{label}</span>
      <span className="text-xl font-semibold text-white truncate">{value}</span>
    </div>
  );
}

function LiveDot({ status }: { status: 'connecting' | 'connected' | 'disconnected' }) {
  if (status === 'connected') {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-emerald-400">
        <span className="relative flex h-2 w-2">
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
          <span className="relative inline-flex rounded-full h-2 w-2 bg-emerald-400" />
        </span>
        Live
      </span>
    );
  }
  if (status === 'connecting') {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-gray-500">
        <span className="w-2 h-2 rounded-full bg-gray-500" />
        Connecting…
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-yellow-500">
      <span className="w-2 h-2 rounded-full bg-yellow-500" />
      Reconnecting…
    </span>
  );
}

interface StationRowProps {
  station: StationSummary;
  onClick: () => void;
  onMouseEnter: () => void;
}

function StationRow({ station, onClick, onMouseEnter }: StationRowProps) {
  return (
    <tr
      onClick={onClick}
      onMouseEnter={onMouseEnter}
      className="border-t border-gray-700 hover:bg-gray-700/50 cursor-pointer transition-colors"
    >
      <td className="px-4 py-3 text-sm font-mono text-gray-300">{station.stationId}</td>
      <td className="px-4 py-3 text-sm text-white font-medium">{station.name}</td>
      <td className="px-4 py-3 text-sm text-gray-400">{station.location || '—'}</td>
      <td className="px-4 py-3 text-sm">
        <span className="inline-flex items-center gap-1.5">
          <span
            className={`w-2 h-2 rounded-full flex-shrink-0 ${
              station.status === 'online' ? 'bg-emerald-400' : 'bg-gray-500'
            }`}
          />
          <span className={station.status === 'online' ? 'text-emerald-400' : 'text-gray-500'}>
            {station.status === 'online' ? 'Online' : 'Offline'}
          </span>
        </span>
      </td>
      <td className="px-4 py-3 text-sm text-gray-300 font-mono whitespace-nowrap">
        {formatDateTime(station.lastReading)}
      </td>
      <td className="px-4 py-3 text-sm text-gray-300">
        {station.lastTemp !== null ? `${station.lastTemp}°C` : '—'}
      </td>
      <td className="px-4 py-3 text-sm text-gray-400 capitalize">
        {station.lastConditions || '—'}
      </td>
      <td className="px-4 py-3 text-sm text-right">
        <span className="text-indigo-400 font-medium">{station.txRecords.toLocaleString()}</span>
        <span className="text-gray-600 ml-1 text-xs">›</span>
      </td>
    </tr>
  );
}

export function Dashboard() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const [page, setPage] = useState(1);
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');

  const { data, isLoading, isFetching, error } = useDashboard({ page, limit: PAGE_SIZE, search });
  const liveStatus = useLiveStats();

  // Prefetch the next page whenever the current page or search changes.
  // React Query deduplicates: if the next page is already fresh in cache, this is a no-op.
  useEffect(() => {
    const totalPages = data?.pagination?.totalPages ?? 0;
    if (page < totalPages) {
      queryClient.prefetchQuery({
        queryKey: ['dashboard', { page: page + 1, limit: PAGE_SIZE, search }],
        queryFn: () => fetchDashboard({ page: page + 1, limit: PAGE_SIZE, search }),
      });
    }
  }, [page, search, data?.pagination?.totalPages, queryClient]);

  // Hover over a station row: prefetch its detail and first page of records.
  // This way clicking into a station is instant — data is already in cache.
  const prefetchStation = useCallback(
    (stationId: number) => {
      queryClient.prefetchQuery({
        queryKey: ['station', stationId],
        queryFn: () => fetchStation(stationId),
      });
      queryClient.prefetchQuery({
        queryKey: ['weather', 'list', { stationId, page: 1, limit: RECORDS_PAGE_SIZE }],
        queryFn: () => fetchWeatherRecords({ stationId, page: 1, limit: RECORDS_PAGE_SIZE }),
      });
    },
    [queryClient]
  );

  const handleSearch = useCallback(() => {
    setSearch(searchInput.trim());
    setPage(1);
  }, [searchInput]);

  const handleSearchKey = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter') handleSearch();
    },
    [handleSearch]
  );

  const handleClear = useCallback(() => {
    setSearchInput('');
    setSearch('');
    setPage(1);
  }, []);

  // Only show a full-page spinner on the very first load (no cached data yet)
  if (isLoading) {
    return (
      <div className="flex justify-center items-center py-24">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-indigo-400" />
      </div>
    );
  }

  if (error && !data) {
    return (
      <div className="text-center py-24">
        <p className="text-red-400">Failed to load dashboard</p>
        <p className="text-gray-500 text-sm mt-2">{(error as Error).message}</p>
      </div>
    );
  }

  const stats = data?.stats;
  const stations = data?.stations ?? [];
  const pagination = data?.pagination;

  return (
    <div className="space-y-5">
      {/* Global Stats */}
      {stats && (
        <div>
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs text-gray-500 uppercase tracking-wide">Global Stats</span>
            <LiveDot status={liveStatus} />
          </div>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            <StatCard label="Active Stations" value={stats.activeStations.toLocaleString()} />
            <StatCard label="Total Tx" value={stats.totalTx.toLocaleString()} />
            <StatCard label="Last Record Write" value={formatDateTime(stats.lastRecordWrite)} />
            <StatCard label="Total Data Points" value={formatLargeNumber(stats.totalDataPoints)} />
          </div>
        </div>
      )}

      {/* Station Table */}
      <div className="bg-gray-800 border border-gray-700 rounded-lg overflow-hidden">
        {/* Toolbar */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-gray-700">
          <div className="flex-1 flex items-center gap-2 max-w-sm">
            <input
              type="text"
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
              onKeyDown={handleSearchKey}
              placeholder="Search by name, location or ID…"
              className="w-full px-3 py-1.5 text-sm bg-gray-900 border border-gray-600 rounded text-gray-200 placeholder-gray-500 focus:outline-none focus:border-indigo-500"
            />
            <button
              onClick={handleSearch}
              className="px-3 py-1.5 text-sm bg-indigo-600 hover:bg-indigo-700 text-white rounded transition-colors whitespace-nowrap"
            >
              Search
            </button>
            {search && (
              <button
                onClick={handleClear}
                className="px-2 py-1.5 text-sm text-gray-400 hover:text-white transition-colors"
              >
                ✕
              </button>
            )}
          </div>
          <div className="ml-auto flex items-center gap-3">
            {/* Subtle fetching indicator — shows when paginating/refetching, not on first load */}
            {isFetching && !isLoading && (
              <div className="w-4 h-4 rounded-full border-2 border-indigo-400 border-t-transparent animate-spin" />
            )}
            {pagination && (
              <span className="text-xs text-gray-500 whitespace-nowrap">
                {pagination.total.toLocaleString()} station{pagination.total !== 1 ? 's' : ''}
              </span>
            )}
          </div>
        </div>

        <div className="overflow-x-auto">
          <table className="w-full text-left">
            <thead>
              <tr className="bg-gray-900/60">
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">Station ID</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">Name</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">Location</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">Status</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">Last Reading</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">Temp</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">Conditions</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide text-right">Tx Records</th>
              </tr>
            </thead>
            {/* Dim the rows during background fetches instead of replacing with a spinner */}
            <tbody className={isFetching && !isLoading ? 'opacity-50 transition-opacity duration-150' : 'transition-opacity duration-150'}>
              {stations.length === 0 ? (
                <tr>
                  <td colSpan={8} className="px-4 py-12 text-center text-gray-500">
                    {search ? `No stations matching "${search}"` : 'No stations found'}
                  </td>
                </tr>
              ) : (
                stations.map((station) => (
                  <StationRow
                    key={station.stationId}
                    station={station}
                    onClick={() => navigate(`/station/${station.stationId}`)}
                    onMouseEnter={() => prefetchStation(station.stationId)}
                  />
                ))
              )}
            </tbody>
          </table>
        </div>

        {/* Pagination footer */}
        {pagination && pagination.totalPages > 1 && (
          <div className="flex items-center justify-between px-4 py-3 border-t border-gray-700">
            <button
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page === 1 || isFetching}
              className="px-3 py-1.5 text-sm text-gray-300 bg-gray-700 hover:bg-gray-600 rounded disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
            >
              &lsaquo; Prev
            </button>
            <span className="text-sm text-gray-400">
              Page {pagination.page} of {pagination.totalPages}
            </span>
            <button
              onClick={() => setPage((p) => Math.min(pagination.totalPages, p + 1))}
              disabled={page === pagination.totalPages || isFetching}
              className="px-3 py-1.5 text-sm text-gray-300 bg-gray-700 hover:bg-gray-600 rounded disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
            >
              Next &rsaquo;
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

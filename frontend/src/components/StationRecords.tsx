import { useState, useEffect, useCallback } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { useStation, useStationRecords } from '../hooks/useWeather';
import { fetchWeatherRecords, fetchWeatherRecord } from '../services/api';
import type { WeatherRecord, RecordStatus } from '../types/weather';

const LIMIT = 20;

function formatDateTime(ts: string | null): string {
  if (!ts) return '—';
  return new Date(ts).toLocaleString('en-GB', {
    day: '2-digit',
    month: '2-digit',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

function truncateTxid(txid: string | null): string {
  if (!txid) return '—';
  return `${txid.slice(0, 6)}...${txid.slice(-4)}`;
}

function getQcStatus(status: RecordStatus): string {
  switch (status) {
    case 'completed':  return 'Post-QC';
    case 'processing': return 'In-QC';
    case 'pending':    return 'Pre-QC';
    case 'failed':     return 'Failed';
  }
}

function getOnChainStatus(record: WeatherRecord): { label: string; color: string } {
  if (record.status !== 'completed' || !record.blockchain.txid) {
    return { label: 'Not On-Chain', color: 'text-gray-500' };
  }
  if (record.blockchain.blockHeight) {
    return { label: 'Confirmed', color: 'text-emerald-400' };
  }
  return { label: 'Unconfirmed', color: 'text-yellow-400' };
}

function QcBadge({ status }: { status: RecordStatus }) {
  const styles: Record<RecordStatus, string> = {
    completed: 'bg-emerald-900/50 text-emerald-400 border border-emerald-700/50',
    processing: 'bg-blue-900/50 text-blue-400 border border-blue-700/50',
    pending:    'bg-yellow-900/50 text-yellow-400 border border-yellow-700/50',
    failed:     'bg-red-900/50 text-red-400 border border-red-700/50',
  };
  return (
    <span className={`inline-flex px-2 py-0.5 rounded text-xs font-medium ${styles[status]}`}>
      {getQcStatus(status)}
    </span>
  );
}

interface TxRowProps {
  record: WeatherRecord;
  onClick: () => void;
  onMouseEnter: () => void;
}

function TxRow({ record, onClick, onMouseEnter }: TxRowProps) {
  const onChain = getOnChainStatus(record);
  return (
    <tr
      onClick={onClick}
      onMouseEnter={onMouseEnter}
      className="border-t border-gray-700 hover:bg-gray-700/50 cursor-pointer transition-colors"
    >
      <td className="px-4 py-3 text-sm font-mono text-gray-300 whitespace-nowrap">
        {formatDateTime(record.timestamp)}
      </td>
      <td className="px-4 py-3 text-sm font-mono">
        {record.blockchain.txid
          ? <span className="text-indigo-400">{truncateTxid(record.blockchain.txid)}</span>
          : <span className="text-gray-600">—</span>}
      </td>
      <td className="px-4 py-3 text-sm font-mono text-gray-300">
        {record.blockchain.blockHeight ?? '—'}
      </td>
      <td className="px-4 py-3 text-sm">
        <QcBadge status={record.status} />
      </td>
      <td className={`px-4 py-3 text-sm font-medium ${onChain.color}`}>
        {onChain.label}
      </td>
    </tr>
  );
}

export function StationRecords() {
  const { stationId: stationIdParam } = useParams<{ stationId: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [page, setPage] = useState(1);

  const stationId = stationIdParam ? parseInt(stationIdParam, 10) : undefined;

  const { data: station, isLoading: stationLoading } = useStation(stationId);
  const {
    data: recordsData,
    isLoading: recordsLoading,
    isFetching,
    error,
  } = useStationRecords(stationId, { page, limit: LIMIT });

  const records = recordsData?.items ?? [];
  const pagination = recordsData?.pagination;

  // Prefetch next page as soon as the current page loads
  useEffect(() => {
    const totalPages = pagination?.totalPages ?? 0;
    if (stationId && page < totalPages) {
      queryClient.prefetchQuery({
        queryKey: ['weather', 'list', { stationId, page: page + 1, limit: LIMIT }],
        queryFn: () => fetchWeatherRecords({ stationId, page: page + 1, limit: LIMIT }),
      });
    }
  }, [page, pagination?.totalPages, stationId, queryClient]);

  // Hover over a tx row: prefetch the detail page so it loads instantly on click
  const prefetchRecord = useCallback(
    (id: string) => {
      queryClient.prefetchQuery({
        queryKey: ['weather', 'detail', id],
        queryFn: () => fetchWeatherRecord(id),
        staleTime: 5 * 60_000, // individual records are immutable once confirmed
      });
    },
    [queryClient]
  );

  const lastBlockHeight =
    station?.lastBlockHeight ??
    records.find((r) => r.blockchain.blockHeight)?.blockchain.blockHeight ??
    null;

  // Only full-page spinner on first load — placeholderData handles subsequent pages
  if ((stationLoading || recordsLoading) && !recordsData) {
    return (
      <div className="flex justify-center items-center py-24">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-indigo-400" />
      </div>
    );
  }

  if (error && !recordsData) {
    return (
      <div className="text-center py-24">
        <p className="text-red-400">Failed to load records</p>
        <p className="text-gray-500 text-sm mt-2">{(error as Error).message}</p>
        <Link to="/" className="mt-4 inline-block text-indigo-400 hover:underline text-sm">
          &larr; Back to dashboard
        </Link>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div>
        <Link to="/" className="text-indigo-400 hover:underline text-sm">
          &larr; Back to dashboard
        </Link>
      </div>

      {/* Station Header */}
      <div className="bg-gray-800 border border-gray-700 rounded-lg px-6 py-5">
        <div>
          <h1 className="text-lg font-semibold text-white">
            Station {stationId}{station?.name ? ` — ${station.name}` : ''}
          </h1>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-1">
            {station?.location && (
              <span className="text-sm text-gray-400">{station.location}</span>
            )}
            <span className="inline-flex items-center gap-1.5 text-sm">
              <span className={`w-2 h-2 rounded-full ${station?.status === 'online' ? 'bg-emerald-400' : 'bg-gray-500'}`} />
              <span className={station?.status === 'online' ? 'text-emerald-400' : 'text-gray-500'}>
                {station?.status === 'online' ? 'Online' : 'Offline'}
              </span>
            </span>
            {station?.lastReading && (
              <span className="text-sm text-gray-400">
                Last reading: {formatDateTime(station.lastReading)}
              </span>
            )}
          </div>
        </div>

        <div className="flex gap-8 mt-4 pt-4 border-t border-gray-700">
          <div>
            <p className="text-xs text-gray-500 uppercase tracking-wide">Total Records</p>
            <p className="text-lg font-semibold text-white mt-0.5">
              {pagination?.total?.toLocaleString() ?? station?.txRecords?.toLocaleString() ?? '—'}
            </p>
          </div>
          <div>
            <p className="text-xs text-gray-500 uppercase tracking-wide">Last Block Written</p>
            <p className="text-lg font-semibold text-white mt-0.5">
              {lastBlockHeight?.toLocaleString() ?? '—'}
            </p>
          </div>
        </div>
      </div>

      {/* Transactions Table */}
      <div className="bg-gray-800 border border-gray-700 rounded-lg overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-left">
            <thead>
              <tr className="bg-gray-900/60">
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">Timestamp</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">TxID</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">Block Height</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">QC Status</th>
                <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">On-Chain Status</th>
              </tr>
            </thead>
            {/* Dim rows during background page fetches rather than blanking the table */}
            <tbody className={isFetching && recordsData ? 'opacity-50 transition-opacity duration-150' : 'transition-opacity duration-150'}>
              {records.length === 0 ? (
                <tr>
                  <td colSpan={5} className="px-4 py-12 text-center text-gray-500">No records found</td>
                </tr>
              ) : (
                records.map((record) => (
                  <TxRow
                    key={record.id}
                    record={record}
                    onClick={() => navigate(`/weather/${record.id}`)}
                    onMouseEnter={() => prefetchRecord(record.id)}
                  />
                ))
              )}
            </tbody>
          </table>
        </div>

        {/* Pagination footer */}
        {pagination && (
          <div className="flex items-center justify-between px-4 py-3 border-t border-gray-700">
            <button
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page === 1 || isFetching}
              className="px-3 py-1.5 text-sm text-gray-300 bg-gray-700 hover:bg-gray-600 rounded disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
            >
              &lsaquo; Prev
            </button>
            <div className="flex items-center gap-3 text-sm text-gray-400">
              {isFetching && (
                <div className="w-3.5 h-3.5 rounded-full border-2 border-indigo-400 border-t-transparent animate-spin" />
              )}
              <span>
                Page {pagination.page} of {pagination.totalPages}
                <span className="ml-4 text-gray-500">Total: {pagination.total.toLocaleString()}</span>
              </span>
            </div>
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

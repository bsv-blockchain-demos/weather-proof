import { useState } from 'react';
import { useWeatherRecords } from '../hooks/useWeather';
import { WeatherCard } from './WeatherCard';
import type { RecordStatus } from '../types/weather';

export function WeatherList() {
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState<RecordStatus | undefined>(undefined);

  const { data, isLoading, error } = useWeatherRecords({
    page,
    limit: 12,
    status,
  });

  if (isLoading) {
    return (
      <div className="flex justify-center items-center py-12">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-indigo-600" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="text-center py-12">
        <p className="text-red-600">Failed to load weather records</p>
        <p className="text-gray-500 text-sm mt-2">{error.message}</p>
      </div>
    );
  }

  const items = data?.items ?? [];
  const pagination = data?.pagination;

  return (
    <div>
      {/* Filters */}
      <div className="mb-6 flex gap-2">
        <select
          value={status ?? ''}
          onChange={(e) => {
            setStatus(e.target.value as RecordStatus || undefined);
            setPage(1);
          }}
          className="px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
        >
          <option value="">All Status</option>
          <option value="pending">Pending</option>
          <option value="processing">Processing</option>
          <option value="completed">Completed</option>
          <option value="failed">Failed</option>
        </select>
      </div>

      {/* Grid or Empty State */}
      {items.length === 0 ? (
        <div className="text-center py-12">
          <p className="text-gray-500">No weather records found</p>
        </div>
      ) : (
        <>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {items.map((record) => (
              <WeatherCard key={record.id} record={record} />
            ))}
          </div>

          {/* Pagination */}
          {pagination && pagination.totalPages > 1 && (
            <div className="mt-6 flex justify-center items-center gap-4">
              <button
                onClick={() => setPage((p) => Math.max(1, p - 1))}
                disabled={page === 1}
                className="px-4 py-2 text-sm font-medium text-gray-700 bg-white border border-gray-300 rounded-md hover:bg-gray-50 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                Previous
              </button>
              <span className="text-sm text-gray-600">
                Page {pagination.page} of {pagination.totalPages}
              </span>
              <button
                onClick={() => setPage((p) => Math.min(pagination.totalPages, p + 1))}
                disabled={page === pagination.totalPages}
                className="px-4 py-2 text-sm font-medium text-gray-700 bg-white border border-gray-300 rounded-md hover:bg-gray-50 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                Next
              </button>
            </div>
          )}

          {/* Summary */}
          {pagination && (
            <div className="mt-4 text-center text-sm text-gray-500">
              Showing {items.length} of {pagination.total} records
            </div>
          )}
        </>
      )}
    </div>
  );
}

import { useParams, Link } from 'react-router-dom';
import { useWeatherRecord } from '../hooks/useWeather';
import { useVerification } from '../hooks/useVerification';
import { VerificationBadge } from './VerificationBadge';
import { getNetwork } from '../services/verify';

/**
 * Format temperature from Celsius
 */
function formatTemp(celsius: number): string {
  return `${celsius.toFixed(1)}°C`;
}

/**
 * Format timestamp
 */
function formatTime(timestamp: string): string {
  return new Date(timestamp).toLocaleString();
}

/**
 * Weather data field groups for display
 */
const fieldGroups = {
  temperature: [
    { key: 'air_temperature', label: 'Air Temperature', format: formatTemp },
    { key: 'feels_like', label: 'Feels Like', format: formatTemp },
    { key: 'dew_point', label: 'Dew Point', format: formatTemp },
    { key: 'wet_bulb_temperature', label: 'Wet Bulb', format: formatTemp },
    { key: 'wet_bulb_globe_temperature', label: 'Wet Bulb Globe', format: formatTemp },
    { key: 'delta_t', label: 'Delta T', format: formatTemp },
  ],
  atmosphere: [
    { key: 'relative_humidity', label: 'Humidity', format: (v: number) => `${Math.round(v)}%` },
    { key: 'station_pressure', label: 'Station Pressure', format: (v: number) => `${v.toFixed(1)} mb` },
    { key: 'sea_level_pressure', label: 'Sea Level Pressure', format: (v: number) => `${v.toFixed(1)} mb` },
    { key: 'pressure_trend', label: 'Pressure Trend', format: (v: string) => v },
    { key: 'air_density', label: 'Air Density', format: (v: number) => `${v.toFixed(4)} kg/m³` },
  ],
  wind: [
    { key: 'wind_avg', label: 'Wind Speed', format: (v: number) => `${v.toFixed(1)} m/s` },
    { key: 'wind_gust', label: 'Wind Gust', format: (v: number) => `${v.toFixed(1)} m/s` },
    { key: 'wind_direction', label: 'Wind Direction', format: (v: number) => `${v}°` },
    { key: 'wind_direction_cardinal', label: 'Cardinal', format: (v: string) => v },
  ],
  solar: [
    { key: 'solar_radiation', label: 'Solar Radiation', format: (v: number) => `${v} W/m²` },
    { key: 'uv', label: 'UV Index', format: (v: number) => v.toFixed(1) },
    { key: 'brightness', label: 'Brightness', format: (v: number) => `${v} lux` },
  ],
  precipitation: [
    { key: 'precip_probability', label: 'Probability', format: (v: number) => `${v}%` },
    { key: 'precip_accum_local_day', label: 'Today', format: (v: number) => `${v.toFixed(2)} mm` },
    { key: 'precip_accum_local_yesterday', label: 'Yesterday', format: (v: number) => `${v.toFixed(2)} mm` },
    { key: 'precip_minutes_local_day', label: 'Minutes Today', format: (v: number) => `${v} min` },
    { key: 'precip_minutes_local_yesterday', label: 'Minutes Yesterday', format: (v: number) => `${v} min` },
  ],
  lightning: [
    { key: 'lightning_strike_count_last_1hr', label: 'Strikes (1hr)', format: (v: number) => v.toString() },
    { key: 'lightning_strike_count_last_3hr', label: 'Strikes (3hr)', format: (v: number) => v.toString() },
    { key: 'lightning_strike_last_distance', label: 'Last Distance', format: (v: number) => `${v} km` },
    { key: 'lightning_strike_last_distance_msg', label: 'Distance Msg', format: (v: string) => v || 'N/A' },
  ],
};

export function WeatherDetail() {
  const { id } = useParams<{ id: string }>();
  const { data: record, isLoading, error } = useWeatherRecord(id);
  const network = getNetwork();

  const txid = record?.blockchain.txid ?? null;
  const { verify, verificationResult, isVerifying, isConfirmed, isCheckingConfirmation } = useVerification(txid);

  if (isLoading) {
    return (
      <div className="flex justify-center items-center py-12">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-indigo-400" />
      </div>
    );
  }

  if (error || !record) {
    return (
      <div className="text-center py-12">
        <p className="text-red-400">Failed to load weather record</p>
        <p className="text-gray-500 text-sm mt-2">{error?.message || 'Record not found'}</p>
        <Link to="/" className="mt-4 inline-block text-indigo-400 hover:underline">
          Back to dashboard
        </Link>
      </div>
    );
  }

  const { data } = record;

  return (
    <div className="max-w-4xl mx-auto">
      {/* Back nav */}
      <div className="mb-6 flex gap-3 text-sm">
        <Link
          to={`/station/${record.stationId}`}
          className="text-indigo-400 hover:underline"
        >
          &larr; Back to station
        </Link>
      </div>

      {/* Main Info Card */}
      <div className="bg-gray-800 rounded-lg border border-gray-700 p-6 mb-6">
        <div className="flex items-start justify-between">
          <div>
            <h1 className="text-3xl font-bold text-white">
              {formatTemp(data.air_temperature)}
            </h1>
            <p className="text-lg text-gray-400 capitalize mt-1">{data.conditions}</p>
            <p className="text-sm text-gray-500 mt-2">
              Station {record.stationId} &middot; {formatTime(record.timestamp)}
            </p>
          </div>
          <div className="flex flex-col items-end gap-2">
            <VerificationBadge
              status={record.status}
              verificationResult={verificationResult}
              isVerifying={isVerifying || isCheckingConfirmation}
              onVerify={record.status === 'completed' && isConfirmed ? verify : undefined}
              isConfirmed={isConfirmed ?? true}
            />
          </div>
        </div>
      </div>

      {/* Blockchain Info */}
      {record.blockchain.txid && (
        <div className="bg-gray-800 rounded-lg border border-gray-700 p-6 mb-6">
          <h2 className="text-base font-semibold text-white mb-4">Blockchain Record</h2>
          <dl className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <dt className="text-sm text-gray-500">Transaction ID</dt>
              <dd className="font-mono text-sm break-all mt-1">
                <a
                  href={`https://${network === 'main' ? 'whatsonchain.com' : 'test.whatsonchain.com'}/tx/${record.blockchain.txid}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-indigo-400 hover:underline"
                >
                  {record.blockchain.txid}
                </a>
              </dd>
            </div>
            <div>
              <dt className="text-sm text-gray-500">Output Index</dt>
              <dd className="font-mono text-sm text-gray-300 mt-1">{record.blockchain.outputIndex}</dd>
            </div>
            <div>
              <dt className="text-sm text-gray-500">Block Height</dt>
              <dd className="font-mono text-sm text-gray-300 mt-1">
                {record.blockchain.blockHeight
                  ? record.blockchain.blockHeight.toLocaleString()
                  : verificationResult?.blockHeight
                    ? verificationResult.blockHeight.toLocaleString()
                    : 'Not confirmed yet'}
              </dd>
            </div>
          </dl>

          {record.status === 'completed' && isConfirmed && !verificationResult && (
            <button
              onClick={verify}
              disabled={isVerifying}
              className="mt-4 px-4 py-2 bg-indigo-600 text-white rounded hover:bg-indigo-700 disabled:opacity-50 transition-colors text-sm"
            >
              {isVerifying ? 'Verifying...' : 'Verify on Blockchain'}
            </button>
          )}

          {verificationResult?.error && (
            <p className="mt-4 text-sm text-red-400">
              Verification error: {verificationResult.error}
            </p>
          )}
        </div>
      )}

      {/* Weather Data Groups */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        {Object.entries(fieldGroups).map(([groupName, fields]) => (
          <div key={groupName} className="bg-gray-800 rounded-lg border border-gray-700 p-6">
            <h2 className="text-sm font-semibold text-gray-300 mb-4 uppercase tracking-wide">{groupName}</h2>
            <dl className="space-y-2.5">
              {fields.map((field) => {
                const value = data[field.key as keyof typeof data];
                return (
                  <div key={field.key} className="flex justify-between">
                    <dt className="text-sm text-gray-500">{field.label}</dt>
                    <dd className="text-sm font-medium text-gray-200">
                      {field.format(value as never)}
                    </dd>
                  </div>
                );
              })}
            </dl>
          </div>
        ))}
      </div>

      {/* Record Metadata */}
      <div className="mt-4 bg-gray-800 rounded-lg border border-gray-700 p-6">
        <h2 className="text-sm font-semibold text-gray-300 mb-4 uppercase tracking-wide">Record Metadata</h2>
        <dl className="grid grid-cols-1 md:grid-cols-3 gap-4">
          <div>
            <dt className="text-sm text-gray-500">Record ID</dt>
            <dd className="font-mono text-xs text-gray-400 mt-1 break-all">{record.id}</dd>
          </div>
          <div>
            <dt className="text-sm text-gray-500">Created At</dt>
            <dd className="text-sm text-gray-300 mt-1">{formatTime(record.createdAt)}</dd>
          </div>
          {record.processedAt && (
            <div>
              <dt className="text-sm text-gray-500">Processed At</dt>
              <dd className="text-sm text-gray-300 mt-1">{formatTime(record.processedAt)}</dd>
            </div>
          )}
        </dl>
        {record.error && (
          <div className="mt-4 p-3 bg-red-900/30 border border-red-700/50 rounded">
            <p className="text-sm text-red-400">{record.error}</p>
          </div>
        )}
      </div>
    </div>
  );
}

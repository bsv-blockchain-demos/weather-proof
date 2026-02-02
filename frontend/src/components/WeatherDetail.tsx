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
  const { verify, verificationResult, isVerifying } = useVerification(txid);

  if (isLoading) {
    return (
      <div className="flex justify-center items-center py-12">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-indigo-600" />
      </div>
    );
  }

  if (error || !record) {
    return (
      <div className="text-center py-12">
        <p className="text-red-600">Failed to load weather record</p>
        <p className="text-gray-500 text-sm mt-2">{error?.message || 'Record not found'}</p>
        <Link to="/" className="mt-4 inline-block text-indigo-600 hover:underline">
          Back to list
        </Link>
      </div>
    );
  }

  const { data } = record;

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-6">
        <Link to="/" className="text-indigo-600 hover:underline text-sm">
          &larr; Back to list
        </Link>
      </div>

      {/* Main Info Card */}
      <div className="bg-white rounded-lg shadow-sm border border-gray-200 p-6 mb-6">
        <div className="flex items-start justify-between">
          <div>
            <h1 className="text-3xl font-bold text-gray-900">
              {formatTemp(data.air_temperature)}
            </h1>
            <p className="text-lg text-gray-600 capitalize">{data.conditions}</p>
            <p className="text-sm text-gray-500 mt-2">
              Station {record.stationId} &middot; {formatTime(record.timestamp)}
            </p>
          </div>
          <div className="flex flex-col items-end gap-2">
            <VerificationBadge
              status={record.status}
              verificationResult={verificationResult}
              isVerifying={isVerifying}
              onVerify={record.status === 'completed' ? verify : undefined}
            />
          </div>
        </div>
      </div>

      {/* Blockchain Info */}
      {record.blockchain.txid && (
        <div className="bg-white rounded-lg shadow-sm border border-gray-200 p-6 mb-6">
          <h2 className="text-lg font-semibold text-gray-900 mb-4">Blockchain Record</h2>
          <dl className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <dt className="text-sm text-gray-500">Transaction ID</dt>
              <dd className="font-mono text-sm break-all">
                <a
                  href={`https://${network === 'main' ? 'whatsonchain.com' : 'test.whatsonchain.com'}/tx/${record.blockchain.txid}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-indigo-600 hover:underline"
                >
                  {record.blockchain.txid}
                </a>
              </dd>
            </div>
            <div>
              <dt className="text-sm text-gray-500">Output Index</dt>
              <dd className="font-mono text-sm">{record.blockchain.outputIndex}</dd>
            </div>
            {verificationResult?.blockHeight && (
              <div>
                <dt className="text-sm text-gray-500">Block Height</dt>
                <dd className="font-mono text-sm">{verificationResult.blockHeight}</dd>
              </div>
            )}
          </dl>

          {record.status === 'completed' && !verificationResult && (
            <button
              onClick={verify}
              disabled={isVerifying}
              className="mt-4 px-4 py-2 bg-indigo-600 text-white rounded-md hover:bg-indigo-700 disabled:opacity-50 transition-colors"
            >
              {isVerifying ? 'Verifying...' : 'Verify on Blockchain'}
            </button>
          )}

          {verificationResult?.error && (
            <p className="mt-4 text-sm text-red-600">
              Verification error: {verificationResult.error}
            </p>
          )}
        </div>
      )}

      {/* Weather Data Groups */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {Object.entries(fieldGroups).map(([groupName, fields]) => (
          <div key={groupName} className="bg-white rounded-lg shadow-sm border border-gray-200 p-6">
            <h2 className="text-lg font-semibold text-gray-900 mb-4 capitalize">{groupName}</h2>
            <dl className="space-y-3">
              {fields.map((field) => {
                const value = data[field.key as keyof typeof data];
                return (
                  <div key={field.key} className="flex justify-between">
                    <dt className="text-sm text-gray-500">{field.label}</dt>
                    <dd className="text-sm font-medium text-gray-900">
                      {field.format(value as never)}
                    </dd>
                  </div>
                );
              })}
            </dl>
          </div>
        ))}
      </div>

      {/* Metadata */}
      <div className="mt-6 bg-white rounded-lg shadow-sm border border-gray-200 p-6">
        <h2 className="text-lg font-semibold text-gray-900 mb-4">Record Metadata</h2>
        <dl className="grid grid-cols-1 md:grid-cols-3 gap-4">
          <div>
            <dt className="text-sm text-gray-500">Record ID</dt>
            <dd className="font-mono text-sm">{record.id}</dd>
          </div>
          <div>
            <dt className="text-sm text-gray-500">Created At</dt>
            <dd className="text-sm">{formatTime(record.createdAt)}</dd>
          </div>
          {record.processedAt && (
            <div>
              <dt className="text-sm text-gray-500">Processed At</dt>
              <dd className="text-sm">{formatTime(record.processedAt)}</dd>
            </div>
          )}
        </dl>
        {record.error && (
          <div className="mt-4 p-3 bg-red-50 rounded-md">
            <p className="text-sm text-red-600">{record.error}</p>
          </div>
        )}
      </div>
    </div>
  );
}

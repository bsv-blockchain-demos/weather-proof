import { Routes, Route, Link } from 'react-router-dom';
import { Dashboard } from './components/Dashboard';
import { StationRecords } from './components/StationRecords';
import { WeatherDetail } from './components/WeatherDetail';

function App() {
  return (
    <div className="min-h-screen bg-gray-900 flex flex-col">
      {/* Header */}
      <header className="bg-gray-900 border-b border-gray-700 sticky top-0 z-10">
        <div className="max-w-7xl mx-auto px-4 py-4 sm:px-6 lg:px-8">
          <Link to="/" className="text-xl font-bold text-white hover:text-indigo-400 transition-colors">
            WeatherProof
          </Link>
        </div>
      </header>

      {/* Main Content */}
      <main className="flex-1 max-w-7xl mx-auto w-full px-4 py-8 sm:px-6 lg:px-8">
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/station/:stationId" element={<StationRecords />} />
          <Route path="/weather/:id" element={<WeatherDetail />} />
        </Routes>
      </main>

      {/* Footer */}
      <footer className="bg-gray-900 border-t border-gray-700 mt-auto">
        <div className="max-w-7xl mx-auto px-4 py-4 sm:px-6 lg:px-8">
          <p className="text-sm text-gray-600 text-center">
            WeatherProof &mdash; BSV Blockchain Weather Data
          </p>
        </div>
      </footer>
    </div>
  );
}

export default App;

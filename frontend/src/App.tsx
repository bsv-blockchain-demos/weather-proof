import { Routes, Route, Link, Outlet } from 'react-router-dom';
import { Dashboard } from './components/Dashboard';
import { StationRecords } from './components/StationRecords';
import { WeatherDetail } from './components/WeatherDetail';
import { LandingPage } from './components/landing/LandingPage';
import { ThemeToggle } from './components/ThemeToggle';
import { ThemeContext, useThemeProvider } from './hooks/useTheme';

function ExplorerLayout() {
  return (
    <div className="min-h-screen bg-storm-50 dark:bg-gray-900 flex flex-col">
      <header className="bg-white dark:bg-gray-900 border-b border-gray-200 dark:border-gray-700 sticky top-0 z-10">
        <div className="max-w-7xl mx-auto px-4 py-4 sm:px-6 lg:px-8 flex items-center justify-between">
          <Link to="/explorer" className="text-xl font-bold text-gray-900 dark:text-white hover:text-indigo-600 dark:hover:text-indigo-400 transition-colors">
            WeatherProof
          </Link>
          <ThemeToggle />
        </div>
      </header>

      <main className="flex-1 max-w-7xl mx-auto w-full px-4 py-8 sm:px-6 lg:px-8">
        <Outlet />
      </main>

      <footer className="bg-white dark:bg-gray-900 border-t border-gray-200 dark:border-gray-700 mt-auto">
        <div className="max-w-7xl mx-auto px-4 py-4 sm:px-6 lg:px-8">
          <p className="text-sm text-gray-400 dark:text-gray-600 text-center">
            WeatherProof &mdash; BSV Blockchain Weather Data
          </p>
        </div>
      </footer>
    </div>
  );
}

function App() {
  const themeValue = useThemeProvider();

  return (
    <ThemeContext.Provider value={themeValue}>
      <Routes>
        <Route path="/" element={<LandingPage />} />
        <Route element={<ExplorerLayout />}>
          <Route path="/explorer" element={<Dashboard />} />
          <Route path="/station/:stationId" element={<StationRecords />} />
          <Route path="/weather/:id" element={<WeatherDetail />} />
        </Route>
      </Routes>
    </ThemeContext.Provider>
  );
}

export default App;

import { Link } from 'react-router-dom';

export function LandingFooter() {
  return (
    <footer className="bg-white dark:bg-storm-900 border-t border-gray-200 dark:border-storm-700">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-12">
        <div className="grid grid-cols-1 md:grid-cols-3 gap-8">
          {/* Brand */}
          <div>
            <span className="font-display font-extrabold text-lg text-storm-950 dark:text-storm-100">
              Weather Proof
            </span>
            <p className="mt-2 text-sm text-storm-600 dark:text-storm-400 max-w-xs">
              Tamper-proof weather data recorded on the BSV blockchain. Open source and verifiable by anyone.
            </p>
          </div>

          {/* Links */}
          <div>
            <h3 className="text-xs font-medium uppercase tracking-wide text-storm-600 dark:text-storm-400 mb-3">Links</h3>
            <ul className="space-y-2 text-sm">
              <li>
                <Link to="/explorer" className="text-storm-800 dark:text-storm-200 hover:text-sunrise-500 transition-colors">
                  Explorer
                </Link>
              </li>
              <li>
                <a href="https://github.com/bsv-blockchain-demos/weather-proof" target="_blank" rel="noopener noreferrer" className="text-storm-800 dark:text-storm-200 hover:text-sunrise-500 transition-colors">
                  GitHub
                </a>
              </li>
              <li>
                <a href="https://www.bsvblockchain.org" target="_blank" rel="noopener noreferrer" className="text-storm-800 dark:text-storm-200 hover:text-sunrise-500 transition-colors">
                  BSV Blockchain
                </a>
              </li>
            </ul>
          </div>

          {/* Built on */}
          <div>
            <h3 className="text-xs font-medium uppercase tracking-wide text-storm-600 dark:text-storm-400 mb-3">Technology</h3>
            <p className="text-sm text-storm-800 dark:text-storm-200">
              Built on BSV Blockchain
            </p>
            <p className="text-sm text-storm-600 dark:text-storm-400 mt-1">
              Overlay Services &middot; SPV Verification
            </p>
          </div>
        </div>

        <div className="mt-10 pt-6 border-t border-gray-200 dark:border-storm-700">
          <p className="text-xs text-storm-600 dark:text-storm-400 text-center">
            Weather Proof &middot; A BSV Blockchain demonstration &middot; Open source
          </p>
        </div>
      </div>
    </footer>
  );
}

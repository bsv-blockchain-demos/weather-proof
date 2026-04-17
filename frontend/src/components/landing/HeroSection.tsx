import { Link } from 'react-router-dom';

export function HeroSection() {
  return (
    <section className="relative min-h-screen flex items-center overflow-hidden bg-storm-50 dark:bg-storm-950 bg-mesh bg-noise">
      <div className="relative z-10 max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-32 w-full">
        <div className="max-w-3xl">
          {/* Live badge */}
          <div className="animate-fade-up mb-6" style={{ animationDelay: '0ms' }}>
            <span className="inline-flex items-center gap-2 px-3 py-1.5 rounded-full text-sm font-medium bg-emerald-900/30 dark:bg-emerald-900/30 text-emerald-700 dark:text-emerald-400 border border-emerald-700/30">
              <span className="relative flex h-2 w-2">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-emerald-400" />
              </span>
              Live on BSV blockchain
            </span>
          </div>

          {/* Headline */}
          <h1
            className="animate-fade-up text-5xl md:text-7xl lg:text-8xl font-display font-extrabold text-storm-950 dark:text-storm-100 leading-[1.1] tracking-tight"
            style={{ animationDelay: '100ms' }}
          >
            Weather data you can{' '}
            <span className="text-sunrise-400">actually</span>{' '}
            trust
          </h1>

          {/* Subline */}
          <p
            className="animate-fade-up mt-6 text-xl md:text-2xl text-storm-600 dark:text-storm-400 max-w-xl leading-relaxed"
            style={{ animationDelay: '200ms' }}
          >
            Every reading recorded on-chain. Every data point independently verifiable. No middlemen, no tampering, no question.
          </p>

          {/* CTAs */}
          <div
            className="animate-fade-up mt-10 flex flex-wrap gap-4"
            style={{ animationDelay: '300ms' }}
          >
            <Link
              to="/explorer"
              className="px-8 py-3.5 rounded-lg bg-sunrise-400 text-storm-950 font-semibold text-lg hover:bg-sunrise-500 transition-colors"
            >
              Explore live data
            </Link>
            <a
              href="https://github.com/bsv-blockchain-demos/weather-proof"
              target="_blank"
              rel="noopener noreferrer"
              className="px-8 py-3.5 rounded-lg border border-storm-700/40 text-storm-800 dark:text-storm-200 font-semibold text-lg hover:bg-storm-800/10 dark:hover:bg-storm-800 transition-colors"
            >
              View source
            </a>
          </div>
        </div>
      </div>
    </section>
  );
}

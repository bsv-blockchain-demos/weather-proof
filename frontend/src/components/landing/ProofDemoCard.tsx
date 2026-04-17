import { useScrollReveal } from '../../hooks/useScrollReveal';

export function ProofDemoCard() {
  const [ref, isVisible] = useScrollReveal();

  return (
    <section
      ref={ref as React.RefObject<HTMLElement>}
      className="relative py-24 bg-storm-50 dark:bg-storm-950 overflow-visible"
    >
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div className="max-w-xl">
          <h2
            className={`text-3xl md:text-4xl font-display font-extrabold text-storm-950 dark:text-storm-100 transition-all duration-700 ${
              isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4'
            }`}
          >
            See the proof in action
          </h2>
          <p
            className={`mt-4 text-lg text-storm-600 dark:text-storm-400 leading-relaxed transition-all duration-700 delay-100 ${
              isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4'
            }`}
          >
            Every weather reading is cryptographically verified against its on-chain record. Here&rsquo;s what that looks like.
          </p>
        </div>

        {/* Demo card */}
        <div
          className={`mt-12 ml-auto max-w-2xl -mb-16 relative z-10 transition-all duration-700 delay-200 ${
            isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-6'
          }`}
        >
          {/* Background glow */}
          <div className="absolute -inset-4 rounded-2xl bg-gradient-radial from-sunrise-400/5 to-transparent blur-xl pointer-events-none" />

          <div className="relative bg-white dark:bg-storm-850 border border-gray-200 dark:border-storm-700 rounded-xl p-6 md:p-8">
            {/* Station info */}
            <div className="flex items-start justify-between gap-4">
              <div>
                <h3 className="font-semibold text-storm-950 dark:text-storm-100">Brix and Columns Vineyard</h3>
                <p className="text-sm text-storm-600 dark:text-storm-400 mt-0.5">
                  Shenandoah Valley, Virginia, USA &middot; Elevation 329m
                </p>
              </div>
              <span className="flex-shrink-0 px-2 py-1 rounded text-xs font-medium bg-emerald-900/30 text-emerald-700 dark:text-emerald-400 border border-emerald-700/30">
                Post-QC
              </span>
            </div>

            {/* Readings */}
            <div className="mt-6 grid grid-cols-2 md:grid-cols-4 gap-4">
              {[
                { label: 'Temperature', value: '18.3°C' },
                { label: 'Humidity', value: '72%' },
                { label: 'Wind', value: '12 km/h' },
                { label: 'Conditions', value: 'Clear' },
              ].map((r) => (
                <div key={r.label}>
                  <p className="text-xs text-storm-600 dark:text-storm-400">{r.label}</p>
                  <p className="text-lg font-semibold text-storm-950 dark:text-storm-100 mt-0.5">{r.value}</p>
                </div>
              ))}
            </div>

            {/* Verification badge */}
            <div className="mt-6 pt-6 border-t border-gray-200 dark:border-storm-700">
              <div className="flex items-center gap-3">
                <span className="animate-glow-pulse-green inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-emerald-900/30 text-emerald-700 dark:text-emerald-400 border border-emerald-700/30">
                  <svg className="w-4 h-4" fill="currentColor" viewBox="0 0 20 20">
                    <path fillRule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clipRule="evenodd" />
                  </svg>
                  Verified on-chain
                </span>
                <span className="text-xs text-storm-600 dark:text-storm-400">Merkle proof valid</span>
              </div>

              <div className="mt-3 flex flex-wrap gap-x-6 gap-y-1 text-xs font-mono text-storm-600 dark:text-storm-400">
                <span>TxID: a3f8c1...9d2e</span>
                <span>Block #879,412</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

import { Link } from 'react-router-dom';
import { useScrollReveal } from '../../hooks/useScrollReveal';

export function FinalCTASection() {
  const [ref, isVisible] = useScrollReveal();

  return (
    <section
      ref={ref as React.RefObject<HTMLElement>}
      className="relative py-32 bg-storm-50 dark:bg-storm-950 overflow-hidden"
    >
      {/* Background glow */}
      <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
        <div className="w-[600px] h-[400px] rounded-full bg-sunrise-400/5 blur-3xl animate-pulse" />
      </div>
      {/* Second frost-tinted blob for color depth */}
      <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
        <div className="w-[500px] h-[350px] rounded-full bg-frost-400/5 blur-2xl animate-pulse translate-x-24 -translate-y-12" style={{ animationDelay: '1.5s' }} />
      </div>

      <div
        className={`relative z-10 max-w-3xl mx-auto px-4 sm:px-6 lg:px-8 text-center transition-all duration-700 ${
          isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-6'
        }`}
      >
        <h2 className="text-3xl md:text-5xl font-display font-extrabold text-storm-950 dark:text-storm-100 leading-tight">
          The weather doesn&rsquo;t lie.
          <br />
          <span className="text-storm-600 dark:text-storm-400">Now, neither does the data.</span>
        </h2>
        <div className="mt-10">
          <Link
            to="/explorer"
            className="inline-block px-10 py-4 rounded-lg bg-sunrise-400 text-storm-950 font-bold text-lg hover:bg-sunrise-500 transition-all duration-200 animate-glow-pulse shadow-lg shadow-sunrise-400/20 hover:shadow-sunrise-400/40 hover:-translate-y-0.5"
          >
            Explore live data
          </Link>
        </div>
      </div>
    </section>
  );
}

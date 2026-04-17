import { useScrollReveal } from '../../hooks/useScrollReveal';

const steps = [
  {
    number: '01',
    title: 'Station records reading',
    description: 'WeatherFlow Tempest stations capture 33 meteorological data points every 5 minutes.',
    tag: 'Hardware',
  },
  {
    number: '02',
    title: 'Data is hashed',
    description: 'Each reading is SHA-256 hashed, creating a unique digital fingerprint of the exact data.',
    tag: 'Integrity',
  },
  {
    number: '03',
    title: 'Written to BSV blockchain',
    description: 'The hash is committed to the BSV blockchain via an overlay service, creating a permanent record.',
    tag: 'Blockchain',
  },
  {
    number: '04',
    title: 'Anyone can verify',
    description: 'Re-hash the data and compare against the on-chain record. If they match, the data is untampered.',
    tag: 'Proof',
  },
];

export function HowItWorksSection() {
  const [ref, isVisible] = useScrollReveal();

  return (
    <section
      ref={ref as React.RefObject<HTMLElement>}
      className="py-24 bg-white dark:bg-storm-900"
    >
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <h2
          className={`text-3xl md:text-4xl font-display font-extrabold text-storm-950 dark:text-storm-100 transition-all duration-700 ${
            isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4'
          }`}
        >
          How it works
        </h2>

        <div className="mt-16 relative">
          {/* Connecting line */}
          <div className="hidden md:block absolute top-6 left-6 right-[calc(25%-2.625rem)] h-px border-t-2 border-dashed border-storm-200 dark:border-storm-700/30" />
          <div className="md:hidden absolute top-0 bottom-0 left-6 w-px border-l-2 border-dashed border-storm-700/30" />

          <div className="grid grid-cols-1 md:grid-cols-4 gap-10 md:gap-6">
            {steps.map((step, i) => (
              <div
                key={step.number}
                className={`relative pl-16 md:pl-0 transition-all duration-700 ${
                  isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-6'
                }`}
                style={{ transitionDelay: `${i * 150}ms` }}
              >
                {/* Number circle */}
                <div
                  className={`absolute left-0 md:relative md:left-auto w-12 h-12 rounded-full bg-sunrise-400 text-storm-950 flex items-center justify-center font-display font-bold text-sm mb-4 hover:ring-2 hover:ring-sunrise-400/40 hover:ring-offset-2 hover:ring-offset-white dark:hover:ring-offset-storm-900 transition-shadow duration-200 ${
                    isVisible ? 'animate-circle-glow' : ''
                  }`}
                  style={{ animationDelay: `${i * 150 + 300}ms` }}
                >
                  {step.number}
                </div>
                <h3 className="text-lg font-semibold text-storm-950 dark:text-storm-100 mt-0 md:mt-4">
                  {step.title}
                </h3>
                <p className="mt-2 text-sm text-storm-600 dark:text-storm-400 leading-relaxed">
                  {step.description}
                </p>
                <span className="inline-block mt-3 px-2.5 py-1 rounded-full text-xs font-medium bg-storm-100 dark:bg-storm-800 text-storm-600 dark:text-storm-400">
                  {step.tag}
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}

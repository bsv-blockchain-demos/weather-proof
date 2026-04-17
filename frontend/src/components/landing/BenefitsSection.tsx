import { useScrollReveal } from '../../hooks/useScrollReveal';

const benefits: { icon: React.ReactNode; title: string; description: string }[] = [
  {
    icon: (
      <svg className="w-8 h-8" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 21V8" />
        <path d="M8 11l4-3 4 3" />
        <path d="M7 7l5-4 5 4" />
        <circle cx="8" cy="15" r="1" />
        <circle cx="12" cy="13" r="1" />
        <circle cx="16" cy="15" r="1" />
        <circle cx="10" cy="18" r="1" />
        <circle cx="14" cy="18" r="1" />
      </svg>
    ),
    title: 'Farmers',
    description: 'Protect crop insurance claims with tamper-proof weather records that insurers can independently verify.',
  },
  {
    icon: (
      <svg className="w-8 h-8" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 2L3 7v6c0 5.25 3.75 10.13 9 11.25 5.25-1.12 9-6 9-11.25V7l-9-5z" />
        <path d="M9 12l2 2 4-4" />
      </svg>
    ),
    title: 'Insurance',
    description: 'Settle claims faster with blockchain-verified weather data. No disputes, no delays, no ambiguity.',
  },
  {
    icon: (
      <svg className="w-8 h-8" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round">
        <circle cx="9" cy="7" r="3" />
        <path d="M9 13c-4 0-6 2-6 4v1h12v-1c0-2-2-4-6-4z" />
        <circle cx="17" cy="8" r="2.5" />
        <path d="M17 13c2.5 0 4 1.5 4 3v1h-5" />
      </svg>
    ),
    title: 'Communities',
    description: 'Build shared, trustworthy weather infrastructure that serves everyone — not just whoever controls the data.',
  },
  {
    icon: (
      <svg className="w-8 h-8" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round">
        <path d="M9 3h6v3H9z" />
        <path d="M10 6v5" />
        <path d="M14 6v5" />
        <path d="M8 11c0 3 1.5 5 4 8 2.5-3 4-5 4-8H8z" />
        <path d="M10 21h4" />
        <path d="M12 19v2" />
      </svg>
    ),
    title: 'Researchers',
    description: 'Access weather data with provable integrity. Every reading has a verifiable chain of custody from sensor to blockchain.',
  },
];

export function BenefitsSection() {
  const [ref, isVisible] = useScrollReveal();

  return (
    <section
      ref={ref as React.RefObject<HTMLElement>}
      className="pt-32 pb-24 bg-white dark:bg-storm-900"
    >
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <h2
          className={`text-3xl md:text-4xl font-display font-extrabold text-right text-storm-950 dark:text-storm-100 transition-all duration-700 ${
            isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4'
          }`}
        >
          Who benefits
        </h2>

        <div className="mt-16 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6">
          {benefits.map((b, i) => (
            <div
              key={b.title}
              className={`group relative overflow-hidden bg-storm-50 dark:bg-storm-850 border border-gray-200 dark:border-storm-700 rounded-xl p-6 transition-all duration-500 hover:border-sunrise-400/30 hover:-translate-y-1 hover:shadow-lg hover:shadow-sunrise-400/10 ${
                isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-6'
              }`}
              style={{ transitionDelay: `${i * 100}ms` }}
            >
              {/* Top accent line */}
              <div className="absolute top-0 left-0 right-0 h-0.5 bg-gradient-to-r from-sunrise-400 to-sunrise-300 opacity-0 group-hover:opacity-100 transition-opacity duration-300" />
              <div className="text-storm-600 dark:text-storm-400 transition-all duration-300 group-hover:text-sunrise-400 group-hover:scale-110 origin-left">{b.icon}</div>
              <h3 className="mt-4 text-lg font-semibold text-storm-950 dark:text-storm-100">{b.title}</h3>
              <p className="mt-2 text-sm text-storm-600 dark:text-storm-400 leading-relaxed">{b.description}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

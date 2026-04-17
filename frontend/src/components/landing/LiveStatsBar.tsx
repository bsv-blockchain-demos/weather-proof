import { useScrollReveal } from '../../hooks/useScrollReveal';

const stats = [
  { value: '19+', label: 'Active stations' },
  { value: '50,000+', label: 'On-chain records' },
  { value: '1M+', label: 'Total Data Points Written' },
  { value: '5 min', label: 'Recording interval' },
];

export function LiveStatsBar() {
  const [ref, isVisible] = useScrollReveal();

  return (
    <section
      ref={ref as React.RefObject<HTMLElement>}
      className="bg-white dark:bg-storm-900 border-y border-gray-200 dark:border-storm-700"
    >
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8 grid grid-cols-2 md:grid-cols-4 gap-6 md:gap-0">
        {stats.map((stat, i) => (
          <div
            key={stat.label}
            className={`text-center transition-all duration-700 ${
              isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4'
            } ${i < stats.length - 1 ? 'md:border-r md:border-storm-700/30' : ''}`}
            style={{ transitionDelay: `${i * 100}ms` }}
          >
            <p className="text-3xl md:text-4xl font-display font-bold bg-gradient-to-r from-sunrise-400 to-sunrise-300 bg-clip-text text-transparent">
              {stat.value}
            </p>
            <p className="mt-1 text-sm text-storm-600 dark:text-storm-400">{stat.label}</p>
          </div>
        ))}
      </div>
    </section>
  );
}

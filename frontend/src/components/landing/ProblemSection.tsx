import { useState, useEffect } from 'react';
import { useScrollReveal } from '../../hooks/useScrollReveal';
import { useCountUp } from '../../hooks/useCountUp';

const problems = [
  {
    numericValue: 58,
    suffix: ' days',
    stat: '58 days',
    title: 'Average insurance claim delay',
    detail: 'Weather disputes stall payouts for months because there\'s no single source of truth both parties trust.',
  },
  {
    numericValue: 40,
    suffix: '%',
    stat: '40%',
    title: 'Of weather data goes unverified',
    detail: 'Stations collect readings, but without immutable records, data integrity is assumed — never proven.',
  },
  {
    numericValue: 0,
    suffix: '',
    stat: '0',
    title: 'Ways to independently verify',
    detail: 'Traditional systems require you to trust the operator. There\'s no public audit trail, no cryptographic proof.',
  },
];

export function ProblemSection() {
  const [ref, isVisible] = useScrollReveal();
  const [wobbleActive, setWobbleActive] = useState(false);

  const count0 = useCountUp(problems[0].numericValue, 1200, isVisible);
  const count1 = useCountUp(problems[1].numericValue, 1200, isVisible);
  const count2 = useCountUp(problems[2].numericValue, 1200, isVisible);
  const counts = [count0, count1, count2];

  useEffect(() => {
    if (!isVisible) return;
    const timer = setTimeout(() => setWobbleActive(true), 240);
    return () => clearTimeout(timer);
  }, [isVisible]);

  return (
    <section
      ref={ref as React.RefObject<HTMLElement>}
      className="py-24 bg-storm-50 dark:bg-storm-950"
    >
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <h2
          className={`text-3xl md:text-4xl font-display font-extrabold text-center text-storm-950 dark:text-storm-100 transition-all duration-700 ${
            isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4'
          }`}
        >
          Weather data shouldn&rsquo;t require a leap of faith
        </h2>

        <div className="mt-16 grid grid-cols-1 sm:grid-cols-3 gap-6">
          {problems.map((p, i) => (
            <div
              key={p.title}
              className={`bg-white dark:bg-storm-850 border border-gray-200 dark:border-storm-700 rounded-xl p-8 transition-all duration-700 ${
                isVisible ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-6'
              }`}
              style={{ transitionDelay: `${i * 120}ms` }}
            >
              <p
                className={`text-4xl font-display font-bold bg-gradient-to-r from-sunrise-400 to-sunrise-300 bg-clip-text text-transparent ${
                  i === 2 && wobbleActive ? 'animate-wobble' : ''
                }`}
              >
                {counts[i]}{p.suffix}
              </p>
              <h3 className="mt-3 text-lg font-semibold text-storm-950 dark:text-storm-100">{p.title}</h3>
              <p className="mt-2 text-sm text-storm-600 dark:text-storm-400 leading-relaxed">{p.detail}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

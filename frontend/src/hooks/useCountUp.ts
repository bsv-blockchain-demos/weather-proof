import { useState, useEffect, useRef } from 'react';

export function useCountUp(end: number, duration: number, isActive: boolean): number {
  const [value, setValue] = useState(0);
  const hasRun = useRef(false);

  useEffect(() => {
    if (!isActive || hasRun.current) return;

    // Respect reduced motion — show final value immediately
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      setValue(end);
      hasRun.current = true;
      return;
    }

    hasRun.current = true;
    const start = performance.now();

    function tick(now: number) {
      const elapsed = now - start;
      const progress = Math.min(elapsed / duration, 1);
      // Cubic ease-out: 1 - (1 - t)^3
      const eased = 1 - Math.pow(1 - progress, 3);
      setValue(Math.round(eased * end));

      if (progress < 1) {
        requestAnimationFrame(tick);
      }
    }

    requestAnimationFrame(tick);
  }, [isActive, end, duration]);

  return value;
}

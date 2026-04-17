import { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';
import { ThemeToggle } from '../ThemeToggle';

export function LandingHeader() {
  const [scrolled, setScrolled] = useState(false);

  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 20);
    window.addEventListener('scroll', onScroll, { passive: true });
    return () => window.removeEventListener('scroll', onScroll);
  }, []);

  return (
    <header
      className={`fixed top-0 left-0 right-0 z-50 transition-all duration-300 ${
        scrolled
          ? 'bg-white/80 dark:bg-storm-950/80 backdrop-blur-lg border-b border-storm-700/20 dark:border-storm-700/40'
          : 'bg-transparent'
      }`}
    >
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 flex items-center justify-between h-16">
        <Link to="/" className="font-display font-extrabold text-xl text-storm-950 dark:text-storm-100 hover:text-sunrise-500 transition-colors">
          Weather Proof
        </Link>
        <div className="flex items-center gap-3">
          <ThemeToggle />
          <Link
            to="/explorer"
            className="px-4 py-2 text-sm font-medium rounded-lg border border-storm-700/40 text-storm-800 dark:text-storm-200 hover:bg-storm-800/10 dark:hover:bg-storm-800 transition-colors"
          >
            Explore Data
          </Link>
        </div>
      </div>
    </header>
  );
}

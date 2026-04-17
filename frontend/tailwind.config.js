/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      fontFamily: {
        display: ['DM Sans', 'system-ui', 'sans-serif'],
        body: ['Outfit', 'system-ui', 'sans-serif'],
      },
      colors: {
        storm: {
          950: '#06080d',
          900: '#0c1018',
          850: '#121824',
          800: '#1a2235',
          700: '#253046',
          600: '#374766',
          400: '#8899b4',
          200: '#c4d0e4',
          100: '#e8edf5',
          50:  '#f4f6fa',
        },
        sunrise: {
          300: '#fcd34d',
          400: '#fbbf24',
          500: '#f59e0b',
        },
        frost: {
          400: '#2dd4bf',
          500: '#14b8a6',
        },
      },
      keyframes: {
        'fade-up': {
          '0%': { opacity: '0', transform: 'translateY(24px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' },
        },
        'glow-pulse': {
          '0%, 100%': { boxShadow: '0 0 8px 0 rgba(251, 191, 36, 0.3)' },
          '50%': { boxShadow: '0 0 20px 4px rgba(251, 191, 36, 0.5)' },
        },
        'glow-pulse-green': {
          '0%, 100%': { boxShadow: '0 0 8px 0 rgba(52, 211, 153, 0.3)' },
          '50%': { boxShadow: '0 0 20px 4px rgba(52, 211, 153, 0.5)' },
        },
        wobble: {
          '0%, 100%': { transform: 'translateX(0)' },
          '15%': { transform: 'translateX(-6px)' },
          '30%': { transform: 'translateX(5px)' },
          '45%': { transform: 'translateX(-4px)' },
          '60%': { transform: 'translateX(3px)' },
          '75%': { transform: 'translateX(-2px)' },
          '90%': { transform: 'translateX(1px)' },
        },
        'circle-glow': {
          '0%, 100%': { boxShadow: '0 0 0 0 rgba(251, 191, 36, 0)' },
          '50%': { boxShadow: '0 0 12px 4px rgba(251, 191, 36, 0.35)' },
        },
      },
      animation: {
        'fade-up': 'fade-up 0.6s ease-out both',
        'glow-pulse': 'glow-pulse 3s ease-in-out infinite',
        'glow-pulse-green': 'glow-pulse-green 3s ease-in-out infinite',
        wobble: 'wobble 0.6s ease-in-out 1',
        'circle-glow': 'circle-glow 2s ease-in-out 3',
      },
    },
  },
  plugins: [],
}

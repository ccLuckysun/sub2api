/** @type {import('tailwindcss').Config} */
const shades = [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]
const primary = Object.fromEntries(
  shades.map(shade => [shade, `rgb(var(--ui-primary-${shade}) / <alpha-value>)`])
)
const gray = {
  50: '#F8F8F9', 100: '#EEEEF0', 200: '#DDDEE3', 300: '#C9CAD1',
  400: '#686A73', 500: '#62636B', 600: '#50515A', 700: '#3C3D46',
  800: '#292A32', 900: '#202126', 950: '#08090B'
}
const dark = {
  50: '#F5F3EF', 100: '#ECE8E1', 200: '#D8D4CE', 300: '#BFBAB3',
  400: '#A6A19A', 500: '#96918B', 600: '#63646E', 700: '#33353D',
  800: '#1A1B20', 900: '#111216', 950: '#08090B'
}

export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: { primary, gray, dark, accent: dark },
      fontFamily: {
        sans: [
          '"Helvetica Neue"', '"Segoe UI"', '"PingFang SC"',
          '"Hiragino Sans GB"', '"Microsoft YaHei"', 'system-ui', 'sans-serif'
        ],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Monaco', 'Consolas', 'monospace']
      },
      boxShadow: {
        glass: 'var(--ui-shadow)',
        'glass-sm': 'var(--ui-shadow-sm)',
        glow: 'var(--ui-shadow-sm)',
        'glow-lg': 'var(--ui-shadow)',
        card: 'var(--ui-shadow)',
        'card-hover': '0 12px 32px var(--ui-shadow-color)',
        'inner-glow': 'inset 0 1px 0 rgb(255 255 255 / .08)',
        neo: 'var(--ui-shadow)'
      },
      backgroundImage: {
        'gradient-radial': 'radial-gradient(var(--tw-gradient-stops))',
        // Retain legacy utility names without reintroducing teal gradients.
        'gradient-primary': 'linear-gradient(var(--ui-accent), var(--ui-accent))',
        'gradient-dark': 'linear-gradient(var(--ui-canvas), var(--ui-canvas))',
        'gradient-glass': 'linear-gradient(var(--ui-surface), var(--ui-surface))',
        'mesh-gradient': 'radial-gradient(circle, rgb(128 128 140 / .1) .7px, transparent .7px)'
      },
      animation: {
        'fade-in': 'fadeIn 240ms var(--ui-ease) both',
        'slide-up': 'slideUp 280ms var(--ui-ease) both',
        'slide-down': 'slideDown 220ms var(--ui-ease) both',
        'slide-in-right': 'slideInRight 240ms var(--ui-ease) both',
        'scale-in': 'scaleIn 180ms var(--ui-ease) both',
        'pulse-slow': 'pulse 3s cubic-bezier(.4, 0, .6, 1) infinite',
        shimmer: 'shimmer 2s linear infinite',
        glow: 'fadeIn 240ms ease-out both'
      },
      keyframes: {
        fadeIn: { '0%': { opacity: '0' }, '100%': { opacity: '1' } },
        slideUp: { '0%': { opacity: '0', transform: 'translateY(6px)' }, '100%': { opacity: '1', transform: 'none' } },
        slideDown: { '0%': { opacity: '0', transform: 'translateY(-6px)' }, '100%': { opacity: '1', transform: 'none' } },
        slideInRight: { '0%': { opacity: '0', transform: 'translateX(12px)' }, '100%': { opacity: '1', transform: 'none' } },
        scaleIn: { '0%': { opacity: '0', transform: 'translateY(4px) scale(.99)' }, '100%': { opacity: '1', transform: 'none' } },
        shimmer: { '0%': { backgroundPosition: '-200% 0' }, '100%': { backgroundPosition: '200% 0' } }
      },
      backdropBlur: { xs: '2px' },
      borderRadius: { '4xl': '1rem' }
    }
  },
  plugins: []
}

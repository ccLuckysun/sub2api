/** @type {import('tailwindcss').Config} */
const shades = [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]
const primary = Object.fromEntries(
  shades.map(shade => [shade, `rgb(var(--neo-primary-${shade}) / <alpha-value>)`])
)
const gray = {
  50: '#fff9e9', 100: '#f6f1e6', 200: '#e3ddcf', 300: '#cfc7b7',
  400: '#867f70', 500: '#6d6657', 600: '#575145', 700: '#403c33',
  800: '#292720', 900: '#111111', 950: '#090909'
}
const dark = {
  50: '#fff9e9', 100: '#eee8d8', 200: '#ddd7c9', 300: '#ccc6b8',
  400: '#bcb7a9', 500: '#969185', 600: '#666258', 700: '#49473f',
  800: '#303030', 900: '#252525', 950: '#191919'
}

export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: { primary, gray, dark, accent: dark },
      fontFamily: {
        sans: [
          '"Avenir Next"', 'Avenir', '"Segoe UI"', '"PingFang SC"',
          '"Hiragino Sans GB"', '"Microsoft YaHei"', 'system-ui', 'sans-serif'
        ],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Monaco', 'Consolas', 'monospace']
      },
      boxShadow: {
        glass: 'var(--neo-shadow)',
        'glass-sm': 'var(--neo-shadow-sm)',
        glow: 'var(--neo-shadow-sm)',
        'glow-lg': 'var(--neo-shadow)',
        card: 'var(--neo-shadow)',
        'card-hover': '5px 5px 0 var(--neo-shadow-color)',
        'inner-glow': 'inset 0 1px 0 rgb(255 255 255 / .08)',
        neo: 'var(--neo-shadow)'
      },
      backgroundImage: {
        'gradient-radial': 'radial-gradient(var(--tw-gradient-stops))',
        // Retain legacy utility names without reintroducing teal gradients.
        'gradient-primary': 'linear-gradient(var(--neo-accent), var(--neo-accent))',
        'gradient-dark': 'linear-gradient(var(--neo-canvas), var(--neo-canvas))',
        'gradient-glass': 'linear-gradient(var(--neo-surface), var(--neo-surface))',
        'mesh-gradient': 'radial-gradient(circle, rgb(128 119 95 / .15) .7px, transparent .7px)'
      },
      animation: {
        'fade-in': 'fadeIn 240ms var(--neo-ease) both',
        'slide-up': 'slideUp 280ms var(--neo-ease) both',
        'slide-down': 'slideDown 220ms var(--neo-ease) both',
        'slide-in-right': 'slideInRight 240ms var(--neo-ease) both',
        'scale-in': 'scaleIn 180ms var(--neo-ease) both',
        'pulse-slow': 'pulse 3s cubic-bezier(.4, 0, .6, 1) infinite',
        shimmer: 'shimmer 2s linear infinite',
        glow: 'fadeIn 240ms ease-out both'
      },
      keyframes: {
        fadeIn: { '0%': { opacity: '0' }, '100%': { opacity: '1' } },
        slideUp: { '0%': { opacity: '0', transform: 'translateY(6px)' }, '100%': { opacity: '1', transform: 'none' } },
        slideDown: { '0%': { opacity: '0', transform: 'translateY(-6px)' }, '100%': { opacity: '1', transform: 'none' } },
        slideInRight: { '0%': { opacity: '0', transform: 'translateX(12px)' }, '100%': { opacity: '1', transform: 'none' } },
        scaleIn: { '0%': { opacity: '0', transform: 'translate(3px, 3px) scale(.99)' }, '100%': { opacity: '1', transform: 'none' } },
        shimmer: { '0%': { backgroundPosition: '-200% 0' }, '100%': { backgroundPosition: '200% 0' } }
      },
      backdropBlur: { xs: '2px' },
      borderRadius: { '4xl': '1rem' }
    }
  },
  plugins: []
}

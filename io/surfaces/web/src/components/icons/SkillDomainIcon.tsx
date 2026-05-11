/** Small domain glyph for skill toasts — no emoji. */

const base = {
  width: 18,
  height: 18,
  viewBox: '0 0 24 24',
  fill: 'none',
  xmlns: 'http://www.w3.org/2000/svg' as const,
  'aria-hidden': true as const,
}

export function SkillDomainIcon({ domain }: { domain: string }) {
  const stroke = 'currentColor'
  const sw = 1.6
  switch (domain) {
    case 'web':
      return (
        <svg {...base}>
          <circle cx="12" cy="12" r="9" stroke={stroke} strokeWidth={sw} />
          <path
            d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18"
            stroke={stroke}
            strokeWidth={sw}
            strokeLinecap="round"
          />
        </svg>
      )
    case 'logs':
      return (
        <svg {...base}>
          <path
            d="M6 4h12v16H6V4Z"
            stroke={stroke}
            strokeWidth={sw}
            strokeLinejoin="round"
          />
          <path d="M9 8h6M9 12h6M9 16h4" stroke={stroke} strokeWidth={sw} strokeLinecap="round" />
        </svg>
      )
    case 'storage':
      return (
        <svg {...base}>
          <ellipse cx="12" cy="6" rx="8" ry="3" stroke={stroke} strokeWidth={sw} />
          <path d="M4 6v6c0 1.7 3.6 3 8 3s8-1.3 8-3V6" stroke={stroke} strokeWidth={sw} />
          <path d="M4 12v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6" stroke={stroke} strokeWidth={sw} />
        </svg>
      )
    case 'data':
      return (
        <svg {...base}>
          <path d="M4 19V5M4 19h16M4 15h16M8 15V9M12 15V5M16 15v-4" stroke={stroke} strokeWidth={sw} strokeLinecap="round" />
        </svg>
      )
    case 'comms':
      return (
        <svg {...base}>
          <path
            d="M12 3a7 7 0 0 0-7 7v3l-2.5 4h19L19 13v-3a7 7 0 0 0-7-7Z"
            stroke={stroke}
            strokeWidth={sw}
            strokeLinejoin="round"
          />
          <path d="M9 18v1a3 3 0 0 0 6 0v-1" stroke={stroke} strokeWidth={sw} strokeLinecap="round" />
        </svg>
      )
    default:
      return (
        <svg {...base}>
          <circle cx="12" cy="12" r="3" stroke={stroke} strokeWidth={sw} />
          <path
            d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"
            stroke={stroke}
            strokeWidth={sw}
            strokeLinecap="round"
          />
        </svg>
      )
  }
}

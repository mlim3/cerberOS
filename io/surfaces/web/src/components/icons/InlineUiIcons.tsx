/** Stroke icons to replace emoji in chat / credential UI. */

interface IconProps {
  className?: string
  size?: number
}

const sw = 1.65

export function IconLock({ className, size = 22 }: IconProps) {
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden
    >
      <rect x="5" y="10" width="14" height="11" rx="2" stroke="currentColor" strokeWidth={sw} />
      <path d="M8 10V8a4 4 0 0 1 8 0v2" stroke="currentColor" strokeWidth={sw} strokeLinecap="round" />
    </svg>
  )
}

export function IconCheckCircle({ className, size = 22 }: IconProps) {
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden
    >
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth={sw} />
      <path
        d="M8 12.5 10.8 15.3 16.2 9.9"
        stroke="currentColor"
        strokeWidth={sw}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

export function IconEye({ className, size = 18 }: IconProps) {
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden
    >
      <path
        d="M2 12s4.5-7 10-7 10 7 10 7-4.5 7-10 7S2 12 2 12Z"
        stroke="currentColor"
        strokeWidth={sw}
        strokeLinejoin="round"
      />
      <circle cx="12" cy="12" r="3" stroke="currentColor" strokeWidth={sw} />
    </svg>
  )
}

export function IconEyeOff({ className, size = 18 }: IconProps) {
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden
    >
      <path
        d="M3 3 21 21M10.6 10.7a3 3 0 0 0 4.7 4.7"
        stroke="currentColor"
        strokeWidth={sw}
        strokeLinecap="round"
      />
      <path
        d="M9.9 4.2C10.6 4.1 11.3 4 12 4c5.5 0 10 6 10 6a18.5 18.5 0 0 1-3.7 4.6M14.1 14.2c-.8.4-1.7.6-2.6.8M6.1 6.1A18.4 18.4 0 0 0 2 12s4.5 6 10 6c1.1 0 2.1-.2 3.1-.5"
        stroke="currentColor"
        strokeWidth={sw}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

export function IconUser({ className, size = 16 }: IconProps) {
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden
    >
      <circle cx="12" cy="8.5" r="3.25" stroke="currentColor" strokeWidth={sw} />
      <path
        d="M6.2 20.5v-.9a5.8 5.8 0 0 1 11.6 0v.9"
        stroke="currentColor"
        strokeWidth={sw}
        strokeLinecap="round"
      />
    </svg>
  )
}

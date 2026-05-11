/** Official CerberOS mark (provided brand SVG). */

interface CerberOsLogoProps {
  className?: string
  /** Accessible label; set false to mark decorative when parent has the name. */
  title?: string | false
}

export function CerberOsLogo({ className, title = 'CerberOS' }: CerberOsLogoProps) {
  const decorative = title === false
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 200 200"
      className={className}
      fill="none"
      aria-hidden={decorative}
      role={decorative ? undefined : 'img'}
      {...(!decorative && title ? { 'aria-label': title } : {})}
    >
      <g stroke="currentColor" strokeWidth="18" fill="none" strokeLinecap="round" strokeLinejoin="round">
        <path d="M 156 43 A 80 80 0 1 0 156 157" />
        <path d="M 100 50 A 50 50 0 0 0 100 150 A 50 50 0 0 0 100 50 A 25 25 0 0 0 100 100 A 25 25 0 0 1 100 150" />
      </g>
    </svg>
  )
}

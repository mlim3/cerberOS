/**
 * Terminal output formatting utilities.
 * Uses ANSI escape codes for colors (cross-platform, no external deps).
 */

export const Colors = {
  reset: '\x1b[0m',
  bold: '\x1b[1m',
  dim: '\x1b[2m',
  red: '\x1b[31m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  magenta: '\x1b[35m',
  cyan: '\x1b[36m',
  white: '\x1b[37m',
}

export const Backgrounds = {
  red: '\x1b[41m',
  green: '\x1b[42m',
  yellow: '\x1b[43m',
  blue: '\x1b[44m',
}

export const cursor = {
  hide: '\x1b[?25l',
  show: '\x1b[?25h',
  up: (n = 1) => `\x1b[${n}A`,
  down: (n = 1) => `\x1b[${n}B`,
  left: (n = 1) => `\x1b[${n}D`,
  right: (n = 1) => `\x1b[${n}C`,
  clearLine: '\x1b[2K',
  clearDown: '\x1b[0J',
}

export const Erase = {
  line: '\x1b[2K',
  screen: '\x1b[2J',
}

export const ansi = {
  bold: (s: string) => `${Colors.bold}${s}${Colors.reset}`,
  dim: (s: string) => `${Colors.dim}${s}${Colors.reset}`,
  red: (s: string) => `${Colors.red}${s}${Colors.reset}`,
  green: (s: string) => `${Colors.green}${s}${Colors.reset}`,
  yellow: (s: string) => `${Colors.yellow}${s}${Colors.reset}`,
  blue: (s: string) => `${Colors.blue}${s}${Colors.reset}`,
  cyan: (s: string) => `${Colors.cyan}${s}${Colors.reset}`,
  magenta: (s: string) => `${Colors.magenta}${s}${Colors.reset}`,

  statusWorking: (s: string) => `${Colors.yellow}⟳${Colors.reset} ${s}`,
  statusDone: (s: string) => `${Colors.green}✓${Colors.reset} ${s}`,
  statusError: (s: string) => `${Colors.red}✗${Colors.reset} ${s}`,
  statusAwaiting: (s: string) => `${Colors.cyan}○${Colors.reset} ${s}`,

  userMessage: (s: string) => `${Colors.blue}you:${Colors.reset} ${s}`,
  agentMessage: (s: string) => `${Colors.green}cerberOS:${Colors.reset} ${s}`,
  systemMessage: (s: string) => `${Colors.dim}${s}${Colors.reset}`,
  credentialRequest: (s: string) => `${Colors.magenta}🔐${Colors.reset} ${s}`,
}

export function spinner(frame: number): string {
  const frames = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']
  return frames[frame % frames.length]
}

export function clearLine(): void {
  process.stdout.write(cursor.clearLine + cursor.left(999))
}

export function progressBar(current: number, total: number, width = 30): string {
  const filled = Math.round((current / total) * width)
  const empty = width - filled
  const bar = `${Colors.green}${'█'.repeat(filled)}${Colors.dim}${'░'.repeat(empty)}${Colors.reset}`
  return `[${bar}] ${current}/${total}`
}

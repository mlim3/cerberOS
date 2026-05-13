/**
 * Lightweight intent detector for user messages that look like cron-style
 * scheduling requests.
 *
 * This keeps `index.ts` from crashing at startup if the module is missing and
 * provides the same coarse gating the chat handler expects before it considers
 * routing a message into user-cron behavior.
 */
export function messageLooksLikeUserCronScheduling(message: string): boolean {
  const text = message.trim().toLowerCase();
  if (!text) return false;

  // Explicit scheduling language.
  if (
    /\b(schedule|scheduling|remind me|set up|create a (?:cron|schedule))\b/.test(
      text,
    )
  ) {
    return true;
  }

  // Common cron-like markers or interval phrasing.
  if (
    /\bcron\b|\bevery\s+\d+\s*(?:second|seconds|minute|minutes|hour|hours|day|days|week|weeks)\b/.test(
      text,
    )
  ) {
    return true;
  }

  // Basic cron expression shape: five fields with common cron symbols.
  if (/^([\d*,\/\-]+\s+){4}[\d*,\/\-]+$/.test(text) && /[\*/,-]/.test(text)) {
    return true;
  }

  return false;
}

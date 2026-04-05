// Shared types for the IO Component
// Extracted from io-interfaces.md

// ============================================
// Task Types
// ============================================

export type TaskStatus = 'awaiting_feedback' | 'working' | 'completed';

export interface Task {
  id: string;
  title: string;
  status: TaskStatus;
  lastUpdate: string;
  expectedNextInput: string;
  messages: ChatMessage[];
}

// ============================================
// Chat / Message Types
// ============================================

export type MessageRole = 'user' | 'assistant' | 'orchestrator';

/** A chat message displayed in the UI */
export interface ChatMessage {
  id: string;
  role: 'user' | 'agent';
  content: string;
  timestamp: string;
  /** When true, content was a credential and should be displayed masked */
  isRedacted?: boolean;
}

// ============================================
// Credential Types (separate from chat pipeline)
// ============================================

/** Orchestrator → IO: request a credential from the user */
export interface CredentialRequest {
  taskId: string;
  /** Unique per request — used for idempotency and correlation */
  requestId: string;
  /** User ID for vault storage. Maps to Memory service's vault namespace. */
  userId: string;
  /** Key name under which the credential will be stored in vault */
  keyName: string;
  /** Human-readable label, e.g. "Production DB password" */
  label: string;
  /** Optional explanation shown to the user */
  description?: string;
}

export type CredentialRequestStatus = 'pending' | 'submitting' | 'submitted' | 'error';

/** IO → Memory Vault: store the credential directly (Orchestrator never sees it) */
export interface CredentialSubmission {
  userId: string;
  keyName: string;
  /** The secret value (sent to Memory, encrypted there) */
  value: string;
}

/** IO → Orchestrator: lightweight ack after secret is stored (no secret material) */
export interface CredentialAck {
  taskId: string;
  requestId: string;
  keyName: string;
  status: 'stored' | 'error';
  error?: string;
}

/** A message in the conversation history sent to the API */
export interface ConversationHistoryItem {
  role: 'user' | 'assistant';
  content: string;
}

export interface SendMessageRequest {
  taskId: string;
  content: string;
  conversationHistory?: ConversationHistoryItem[];
}

// ============================================
// Status Updates
// ============================================

export interface StatusUpdate {
  taskId: string;
  status: TaskStatus;
  lastUpdate: string;
  expectedNextInputMinutes: number | null;
  timestamp?: string | number;
}

// ============================================
// Orchestrator → IO push stream (SSE / WebSocket)
// ============================================

/** One frame on the orchestrator→IO push channel (per task stream). */
export type OrchestratorStreamEvent =
  | { type: 'status'; payload: StatusUpdate }
  | { type: 'credential_request'; payload: CredentialRequest };

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null;
}

function isTaskStatus(x: unknown): x is TaskStatus {
  return x === 'awaiting_feedback' || x === 'working' || x === 'completed';
}

/** Parse SSE `data:` JSON into a stream event. Supports legacy bare `StatusUpdate` objects. */
export function parseOrchestratorStreamEvent(raw: unknown): OrchestratorStreamEvent | null {
  if (!isRecord(raw)) return null;

  if (raw.type === 'status' && isRecord(raw.payload)) {
    const p = raw.payload;
    if (
      typeof p.taskId === 'string' &&
      isTaskStatus(p.status) &&
      typeof p.lastUpdate === 'string' &&
      (p.expectedNextInputMinutes === null || typeof p.expectedNextInputMinutes === 'number')
    ) {
      return { type: 'status', payload: p as unknown as StatusUpdate };
    }
    return null;
  }

  if (raw.type === 'credential_request' && isRecord(raw.payload)) {
    const p = raw.payload;
    if (
      typeof p.taskId === 'string' &&
      typeof p.requestId === 'string' &&
      typeof p.userId === 'string' &&
      typeof p.keyName === 'string' &&
      typeof p.label === 'string' &&
      (p.description === undefined || typeof p.description === 'string')
    ) {
      return { type: 'credential_request', payload: p as unknown as CredentialRequest };
    }
    return null;
  }

  // Legacy: top-level StatusUpdate (no envelope)
  if (
    typeof raw.taskId === 'string' &&
    isTaskStatus(raw.status) &&
    typeof raw.lastUpdate === 'string' &&
    (raw.expectedNextInputMinutes === null || typeof raw.expectedNextInputMinutes === 'number')
  ) {
    return { type: 'status', payload: raw as unknown as StatusUpdate };
  }

  return null;
}

// ============================================
// Memory / Logging Types
// ============================================

export interface LogEntry {
  taskId: string;
  role: 'user' | 'orchestrator';
  content: string;
  at: string; // ISO 8601
}

// ============================================
// Surface Adapter Types
// ============================================

/** Capabilities that a surface may support */
export interface SurfaceCapabilities {
  text: boolean;
  voice: boolean;
  image: boolean;
  video: boolean;
  richCards: boolean;
  pushNotifications: boolean;
  voiceCalls: boolean;
}

/** Input types that a surface can receive */
export type InputType = 'text' | 'voice' | 'image' | 'video';

/** Processed input from the user */
export interface ProcessedInput {
  type: InputType;
  content: string;
  taskId?: string;
  metadata?: Record<string, unknown>;
  // Voice-specific:
  audioDurationMs?: number;
  transcriptionConfidence?: number;
}

/** Agent response to deliver to the user */
export interface AgentResponse {
  taskId: string;
  content: string;
  isRichCard?: boolean;
  streamComplete?: boolean;
}

/** Notification to deliver to the user */
export interface Notification {
  id: string;
  priority: 'urgent' | 'important' | 'normal';
  title: string;
  body: string;
  taskId?: string;
  actionUrl?: string;
}

/** The Surface Adapter interface - all surfaces implement this */
export interface SurfaceAdapter {
  readonly surfaceId: string;
  readonly surfaceName: string;
  readonly surfaceType: string;
  readonly capabilities: SurfaceCapabilities;

  initialize(): Promise<void>;
  receiveInput(input: ProcessedInput): void;
  showTaskStatus(update: StatusUpdate): void;
  deliverResponse(response: AgentResponse): void;
  notify(notification: Notification): void;
  getTasks(): Task[];
  getConversationHistory(taskId: string): ChatMessage[];
  shutdown(): Promise<void>;
}

export type SurfaceType = 'web' | 'cli' | 'telegram' | 'mobile';

/** Default capabilities for a basic text-only surface */
export const BASE_CAPABILITIES: SurfaceCapabilities = {
  text: true,
  voice: false,
  image: false,
  video: false,
  richCards: false,
  pushNotifications: false,
  voiceCalls: false,
};

/** Capabilities for a full-featured web dashboard */
export const WEB_DASHBOARD_CAPABILITIES: SurfaceCapabilities = {
  text: true,
  voice: true,
  image: true,
  video: true,
  richCards: true,
  pushNotifications: false,
  voiceCalls: false,
};

// ============================================
// Settings Types
// ============================================

export interface IOSettings {
  demoMode: boolean;
  showStreamingIndicator: boolean;
  showActivityLog: boolean;
  taskListSortBy: 'urgency' | 'recent' | 'status';
  showHeartbeat: boolean;
  fontSize: 'small' | 'medium' | 'large';
  highContrast: boolean;
}

export const DEFAULT_SETTINGS: IOSettings = {
  demoMode: true,
  showStreamingIndicator: true,
  showActivityLog: true,
  taskListSortBy: 'urgency',
  showHeartbeat: true,
  fontSize: 'medium',
  highContrast: false,
};

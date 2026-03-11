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

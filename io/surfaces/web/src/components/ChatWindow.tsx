import { useState, useRef, useEffect, useMemo, type ReactNode } from 'react'
import { marked } from 'marked'
import type { Task, CredentialRequest, CredentialRequestStatus, ChatMessage } from '@cerberos/io-core'
import type { UISettings } from './SettingsPanel'
import CredentialRequestCard from './CredentialRequestCard'
import ProgressIndicator from './ProgressIndicator'
import { VoiceRecorder } from './VoiceRecorder'
import { CerberOsLogo } from './icons/CerberOsLogo'
import { IconUser } from './icons/InlineUiIcons'
import { inferAgentLane } from '../lib/infer-agent-lane'
import { splitAgentTaskStatusPreamble } from '../lib/split-agent-task-preamble'
import { stripTaskCompleteDisplayNoise } from '../lib/strip-task-complete-display'
import './ChatWindow.css'
import './VoiceRecorder.css'

marked.setOptions({ breaks: true, gfm: true })

/** Open markdown links in a new tab; noopener/noreferrer for window.opener safety. */
marked.use({
  hooks: {
    postprocess(html) {
      return html.replaceAll('<a ', '<a target="_blank" rel="noopener noreferrer" ')
    },
  },
})

function MarkdownContent({ content }: { content: string }) {
  const cleaned = useMemo(() => stripTaskCompleteDisplayNoise(content), [content])
  const { preamble, body } = useMemo(() => splitAgentTaskStatusPreamble(cleaned), [cleaned])
  const bodyHtml = useMemo(
    () => (body.trim() ? (marked.parse(body) as string) : ''),
    [body],
  )
  const fullHtml = useMemo(() => marked.parse(cleaned) as string, [cleaned])

  if (!preamble) {
    return <div className="message-text markdown-body" dangerouslySetInnerHTML={{ __html: fullHtml }} />
  }

  return (
    <div className="message-text markdown-body">
      <div className="agent-task-status" aria-label="Orchestrator status">
        {preamble.split('\n').map((line, i) => (
          <p key={i} className="agent-task-status-line">
            {line}
          </p>
        ))}
      </div>
      {bodyHtml ? (
        <div className="agent-markdown-body" dangerouslySetInnerHTML={{ __html: bodyHtml }} />
      ) : null}
    </div>
  )
}

interface ChatWindowProps {
  task: Task
  onSendMessage: (taskId: string, content: string) => void | Promise<void>
  isStreaming: boolean
  streamingContent: string
  settings: UISettings
  credentialRequest?: CredentialRequest | null
  credentialStatus?: CredentialRequestStatus
  onProvideCredential?: () => void
  /** Rendered below the transcript, above the composer (e.g. recurring schedule panel). */
  belowMessages?: ReactNode
  /** Lock the composer (voice + text). */
  composerDisabled?: boolean
  composerDisabledHint?: string
  inputPlaceholder?: string
  pulseMessageKey?: string
}

const SUGGESTION_CHIPS = [
  'Approve this plan',
  'Request a summary',
  'Ask for alternatives',
  'Show me the risks',
  'Proceed with changes',
]

type TranscriptDisplayLane = 'user' | 'assistant' | 'sub_agent' | 'system' | 'thinking'

function transcriptLane(message: ChatMessage): Exclude<TranscriptDisplayLane, 'thinking'> {
  if (message.role === 'user') return message.isRedacted ? 'system' : 'user'
  if (message.lane === 'system') return 'system'
  if (message.lane === 'sub_agent') return 'sub_agent'
  if (message.lane === 'assistant') return 'assistant'
  return inferAgentLane(message.content)
}

function MessageSenderRow({ lane }: { lane: TranscriptDisplayLane }) {
  if (lane === 'user') {
    return <span className="message-sender message-sender--plain">You</span>
  }
  if (lane === 'system') {
    return <span className="message-sender message-sender--plain">You</span>
  }
  if (lane === 'thinking') {
    return (
      <span className="message-sender message-sender--agent">
        <CerberOsLogo className="message-sender-logo" title={false} />
        Thinking
      </span>
    )
  }
  const label = lane === 'sub_agent' ? 'Sub-agent' : 'Assistant'
  return (
    <span className="message-sender message-sender--agent">
      <CerberOsLogo className="message-sender-logo" title={false} />
      {label}
    </span>
  )
}

function ChatWindow({
  task,
  onSendMessage,
  isStreaming,
  streamingContent,
  settings,
  credentialRequest,
  credentialStatus,
  onProvideCredential,
  belowMessages,
  composerDisabled = false,
  composerDisabledHint,
  inputPlaceholder,
  pulseMessageKey,
}: ChatWindowProps) {
  const [inputValue, setInputValue] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const messagesEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [task.messages, streamingContent])

  useEffect(() => {
    setInputValue('')
  }, [task.id])

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!inputValue.trim() || isStreaming || composerDisabled) return
    const text = inputValue.trim()
    setInputValue('')
    onSendMessage(task.id, text)
  }

  const handleChipClick = (text: string) => {
    if (isStreaming || composerDisabled) return
    setInputValue(text)
    inputRef.current?.focus()
  }

  return (
    <div className="chat-window">
      <div className="chat-status-bar">
        <div className="status-indicator">
          {task.status === 'awaiting_feedback' && (
            <>
              <span className="status-dot warning"></span>
              <span>Awaiting your feedback</span>
            </>
          )}
          {task.status === 'working' && (
            <>
              <span className="status-dot working"></span>
              <span>{task.lastUpdate}</span>
            </>
          )}
          {task.status === 'completed' && (
            <>
              <span className="status-dot completed"></span>
              <span>Completed — ask anything to continue</span>
            </>
          )}
        </div>
        <div className="next-input">
          Next input: <strong>{task.expectedNextInput}</strong>
        </div>
      </div>

      {isStreaming && settings.showStreamingProgress && (
        <div className="streaming-progress-bar">
          <div className="streaming-progress-fill"></div>
        </div>
      )}

      <div className="messages-container">
        {task.messages.map(message => {
          const lane = transcriptLane(message)
          const agentLaneClass = message.role === 'agent' ? ` message-lane-${lane}` : ''
          return (
          <div
            key={message.id}
            className={`message ${message.role}${agentLaneClass}${message.isRedacted ? ' redacted' : ''}${
              message.scheduledRun ? ' message-scheduled-run' : ''
            }${pulseMessageKey && pulseMessageKey === message.id ? ' message-pulse-new' : ''}`}
          >
            <div className="message-avatar">
              {message.role === 'user' ? (
                <IconUser className="message-avatar-user" size={15} />
              ) : (
                <span className="avatar-glyph">C</span>
              )}
            </div>
            <div className="message-content">
              <div className="message-header">
                <MessageSenderRow lane={lane} />
                {message.scheduledRun && (
                  <span className="scheduled-turn-badge" title="Automated run from your schedule">
                    Scheduled
                  </span>
                )}
                <span className="message-time">{message.timestamp}</span>
                {message.isRedacted && (
                  <span className="redacted-badge">Secure</span>
                )}
              </div>
              {message.role === 'agent'
                ? <MarkdownContent content={message.content} />
                : <div className="message-text">{message.content}</div>
              }
            </div>
          </div>
          )
        })}

        {credentialRequest && credentialStatus && onProvideCredential && (
          <CredentialRequestCard
            request={credentialRequest}
            status={credentialStatus}
            onProvide={onProvideCredential}
          />
        )}

        {isStreaming && (
          <div className="message agent streaming message-lane-thinking">
            <div className="message-avatar"><span className="avatar-glyph">C</span></div>
            <div className="message-content">
              <div className="message-header">
                <MessageSenderRow lane="thinking" />
                <span className="message-time">{new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>
                <span className="streaming-badge">Streaming</span>
              </div>
              <div className="message-text">
                {streamingContent || '…'}
                <span className="streaming-cursor">▌</span>
              </div>
            </div>
          </div>
        )}
        <ProgressIndicator
          isActive={isStreaming || task.status === 'working'}
          statusText={isStreaming ? undefined : task.lastUpdate}
          className={isStreaming ? 'progress-indicator--thinking' : 'progress-indicator--working'}
        />
        <div ref={messagesEndRef} />
      </div>

      {belowMessages}

      <div className="chat-input-area">
        {settings.demoMode && !isStreaming && !composerDisabled && !(task.title === 'New Task' && task.messages.length === 0) && (
          <div className="suggestion-chips">
            {SUGGESTION_CHIPS.map(chip => (
              <button
                key={chip}
                type="button"
                className="suggestion-chip"
                onClick={() => handleChipClick(chip)}
              >
                {chip}
              </button>
            ))}
          </div>
        )}
        {composerDisabledHint && composerDisabled && (
          <p className="composer-locked-hint" role="status">
            {composerDisabledHint}
          </p>
        )}
        <form className="chat-input-form" onSubmit={handleSubmit}>
          <VoiceRecorder
            onTranscription={text => {
              if (!composerDisabled && !isStreaming) onSendMessage(task.id, text)
            }}
            disabled={isStreaming || composerDisabled}
          />
          <input
            ref={inputRef}
            type="text"
            value={inputValue}
            onChange={e => setInputValue(e.target.value)}
            placeholder={
              inputPlaceholder ??
              (task.messages.length === 0 ? 'Describe the new task…' : 'Type your response...')
            }
            className="chat-input"
            disabled={composerDisabled}
            aria-disabled={composerDisabled}
          />
          <button type="submit" className="send-button" disabled={isStreaming || composerDisabled}>
            {isStreaming ? '…' : 'Send'}
          </button>
        </form>
      </div>
    </div>
  )
}

export default ChatWindow

import { useState, useRef, useEffect, useMemo, type ReactNode } from 'react'
import { marked } from 'marked'
import type { Task, CredentialRequest, CredentialRequestStatus } from '@cerberos/io-core'
import type { UISettings } from './SettingsPanel'
import CredentialRequestCard from './CredentialRequestCard'
import { VoiceRecorder } from './VoiceRecorder'
import './ChatWindow.css'
import './VoiceRecorder.css'

marked.setOptions({ breaks: true, gfm: true })

function MarkdownContent({ content }: { content: string }) {
  const html = useMemo(() => marked.parse(content) as string, [content])
  return <div className="message-text markdown-body" dangerouslySetInnerHTML={{ __html: html }} />
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
        {task.messages.map(message => (
          <div
            key={message.id}
            className={`message ${message.role}${message.isRedacted ? ' redacted' : ''}${
              message.scheduledRun ? ' message-scheduled-run' : ''
            }${pulseMessageKey && pulseMessageKey === message.id ? ' message-pulse-new' : ''}`}
          >
            <div className="message-avatar">
              {message.role === 'user' ? '👤' : <span className="avatar-glyph">C</span>}
            </div>
            <div className="message-content">
              <div className="message-header">
                <span className="message-sender">
                  {message.role === 'user' ? 'You' : 'cerberOS'}
                </span>
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
        ))}

        {credentialRequest && credentialStatus && onProvideCredential && (
          <CredentialRequestCard
            request={credentialRequest}
            status={credentialStatus}
            onProvide={onProvideCredential}
          />
        )}

        {isStreaming && (
          <div className="message agent streaming">
            <div className="message-avatar"><span className="avatar-glyph">C</span></div>
            <div className="message-content">
              <div className="message-header">
                <span className="message-sender">cerberOS</span>
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

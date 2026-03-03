import { useState } from 'react'
import type { Task } from '../App'
import './ChatWindow.css'

interface ChatWindowProps {
  task: Task
}

function ChatWindow({ task }: ChatWindowProps) {
  const [inputValue, setInputValue] = useState('')

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!inputValue.trim()) return
    console.log('Sending message:', inputValue)
    setInputValue('')
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
              <span>Task completed</span>
            </>
          )}
        </div>
        <div className="next-input">
          Next input: <strong>{task.expectedNextInput}</strong>
        </div>
      </div>

      <div className="messages-container">
        {task.messages.map(message => (
          <div key={message.id} className={`message ${message.role}`}>
            <div className="message-avatar">
              {message.role === 'user' ? '👤' : '🤖'}
            </div>
            <div className="message-content">
              <div className="message-header">
                <span className="message-sender">
                  {message.role === 'user' ? 'You' : 'cerberOS'}
                </span>
                <span className="message-time">{message.timestamp}</span>
              </div>
              <div className="message-text">{message.content}</div>
            </div>
          </div>
        ))}
      </div>

      <form className="chat-input-form" onSubmit={handleSubmit}>
        <input
          type="text"
          value={inputValue}
          onChange={e => setInputValue(e.target.value)}
          placeholder="Type your response..."
          className="chat-input"
        />
        <button type="submit" className="send-button">
          Send
        </button>
      </form>
    </div>
  )
}

export default ChatWindow

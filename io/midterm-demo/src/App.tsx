import { useState } from 'react'
import TaskSidebar from './components/TaskSidebar'
import ChatWindow from './components/ChatWindow'
import SettingsButton from './components/SettingsButton'
import './App.css'

export interface Task {
  id: string
  title: string
  status: 'awaiting_feedback' | 'working' | 'completed'
  lastUpdate: string
  expectedNextInput: string
  messages: Message[]
}

export interface Message {
  id: string
  role: 'user' | 'agent'
  content: string
  timestamp: string
}

const mockTasks: Task[] = [
  {
    id: '1',
    title: 'Implement authentication flow',
    status: 'awaiting_feedback',
    lastUpdate: 'Awaiting user approval for OAuth provider selection',
    expectedNextInput: 'Now',
    messages: [
      { id: '1a', role: 'user', content: 'Set up authentication for the app', timestamp: '10:30 AM' },
      { id: '1b', role: 'agent', content: 'I\'ve analyzed the requirements. For authentication, I recommend implementing OAuth 2.0 with support for Google and GitHub providers. Should I proceed with this approach, or would you prefer a different authentication method?', timestamp: '10:31 AM' },
    ],
  },
  {
    id: '2',
    title: 'Refactor database schema',
    status: 'awaiting_feedback',
    lastUpdate: 'Migration script ready for review',
    expectedNextInput: 'Now',
    messages: [
      { id: '2a', role: 'user', content: 'Optimize the user table for better query performance', timestamp: '9:15 AM' },
      { id: '2b', role: 'agent', content: 'I\'ve created a migration script that adds indexes to the user table and normalizes the address fields. The migration is ready for your review before execution.', timestamp: '9:45 AM' },
    ],
  },
  {
    id: '3',
    title: 'Build dashboard components',
    status: 'working',
    lastUpdate: 'Creating chart components...',
    expectedNextInput: '~5 min',
    messages: [
      { id: '3a', role: 'user', content: 'Create a dashboard with user metrics and activity charts', timestamp: '11:00 AM' },
      { id: '3b', role: 'agent', content: 'Working on the dashboard components. Currently implementing the activity chart using recharts library...', timestamp: '11:02 AM' },
    ],
  },
  {
    id: '4',
    title: 'API endpoint testing',
    status: 'working',
    lastUpdate: 'Running integration tests...',
    expectedNextInput: '~12 min',
    messages: [
      { id: '4a', role: 'user', content: 'Write comprehensive tests for all REST endpoints', timestamp: '8:00 AM' },
      { id: '4b', role: 'agent', content: 'I\'m systematically testing each endpoint. Currently on the user management endpoints. 15 of 32 tests completed.', timestamp: '8:30 AM' },
    ],
  },
  {
    id: '5',
    title: 'Documentation update',
    status: 'completed',
    lastUpdate: 'Documentation published',
    expectedNextInput: 'Done',
    messages: [
      { id: '5a', role: 'user', content: 'Update the API documentation with new endpoints', timestamp: 'Yesterday' },
      { id: '5b', role: 'agent', content: 'Documentation has been updated with all new endpoints, including request/response examples and authentication requirements. The docs are now published.', timestamp: 'Yesterday' },
    ],
  },
]

function App() {
  const [tasks] = useState<Task[]>(mockTasks)
  const [selectedTaskId, setSelectedTaskId] = useState<string>(mockTasks[0].id)
  const [showSettings, setShowSettings] = useState(false)

  const selectedTask = tasks.find(t => t.id === selectedTaskId)

  return (
    <div className="app">
      <TaskSidebar
        tasks={tasks}
        selectedTaskId={selectedTaskId}
        onSelectTask={setSelectedTaskId}
      />
      <main className="main-content">
        <header className="header">
          <h1 className="header-title">
            {selectedTask?.title || 'Select a task'}
          </h1>
          <SettingsButton
            isOpen={showSettings}
            onToggle={() => setShowSettings(!showSettings)}
          />
        </header>
        {selectedTask && <ChatWindow task={selectedTask} />}
        {showSettings && (
          <div className="settings-panel">
            <h2>Settings</h2>
            <div className="settings-item">
              <label>API Key</label>
              <input type="password" placeholder="sk-ant-..." />
            </div>
            <div className="settings-item">
              <label>Model</label>
              <select>
                <option>claude-3-opus</option>
                <option>claude-3-sonnet</option>
                <option>claude-3-haiku</option>
              </select>
            </div>
          </div>
        )}
      </main>
    </div>
  )
}

export default App

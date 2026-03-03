import type { Task } from '../App'
import './TaskSidebar.css'

interface TaskSidebarProps {
  tasks: Task[]
  selectedTaskId: string
  onSelectTask: (id: string) => void
}

function TaskSidebar({ tasks, selectedTaskId, onSelectTask }: TaskSidebarProps) {
  const sortedTasks = [...tasks].sort((a, b) => {
    const priority = { awaiting_feedback: 0, working: 1, completed: 2 }
    return priority[a.status] - priority[b.status]
  })

  return (
    <aside className="sidebar">
      <div className="sidebar-header">
        <h2>Tasks</h2>
        <span className="task-count">{tasks.length}</span>
      </div>
      <div className="task-list">
        {sortedTasks.map(task => (
          <button
            key={task.id}
            className={`task-item ${selectedTaskId === task.id ? 'selected' : ''}`}
            onClick={() => onSelectTask(task.id)}
          >
            <div className="task-status">
              {task.status === 'awaiting_feedback' && (
                <span className="status-icon warning" title="Awaiting feedback">⚠️</span>
              )}
              {task.status === 'working' && (
                <span className="status-icon working" title="Working">
                  <span className="spinner"></span>
                </span>
              )}
              {task.status === 'completed' && (
                <span className="status-icon completed" title="Completed">✓</span>
              )}
            </div>
            <div className="task-info">
              <span className="task-title">{task.title}</span>
              <span className="task-update">{task.lastUpdate}</span>
            </div>
            <div className="task-eta">
              <span className={task.status === 'awaiting_feedback' ? 'urgent' : ''}>
                {task.expectedNextInput}
              </span>
            </div>
          </button>
        ))}
      </div>
    </aside>
  )
}

export default TaskSidebar

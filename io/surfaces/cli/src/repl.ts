/**
 * Interactive REPL for the cerberOS CLI surface.
 * - Lines starting with / are commands
 * - All other lines are sent as messages to the active task
 */

import { createInterface } from 'readline'
import { ansi, Colors, clearLine } from './ui/formatter'
import { streamChat, fetchTasks, fetchTaskLogs } from './io/orchestrator-client'
import { submitCredential } from './io/memory-client'
import { subscribeToTask, type SSEClient } from './io/sse-client'
import { SessionStore } from './session/store'
import type { CLIConfig } from './config'
import type { StatusUpdate, CredentialRequest } from '@cerberos/io-core'

const rl = createInterface({
  input: process.stdin,
  output: process.stdout,
  prompt: `${ansi.blue('>')}${Colors.reset} `,
  completer: (line: string) => {
    const commands = ['/tasks', '/task', '/credential', '/history', '/status', '/quit', '/exit', '/help']
    const hits = commands.filter(c => c.startsWith(line))
    return [hits.length ? hits : [], line]
  },
})

export class CerberOSREPL {
  private config: CLIConfig
  private store: SessionStore
  private sseClients: Map<string, { disconnect(): void }> = new Map()
  private pendingCredentials: Map<string, CredentialRequest> = new Map()
  private currentTaskId?: string
  private isStreaming = false

  constructor(config: CLIConfig) {
    this.config = config
    this.store = new SessionStore(config.sessionFile)
  }

  async start(): Promise<void> {
    await this.store.load()
    this.currentTaskId = this.store.getActiveTaskId()

    // Header
    console.log(ansi.bold(`${Colors.cyan}╔══════════════════════════════════════╗${Colors.reset}`))
    console.log(ansi.bold(`${Colors.cyan}║${Colors.reset}  ${Colors.bold}cerberOS CLI${Colors.reset}                   ${Colors.cyan}║${Colors.reset}`))
    console.log(ansi.bold(`${Colors.cyan}║${Colors.reset}  AI Agent Task Management Surface ${Colors.cyan}║${Colors.reset}`))
    console.log(ansi.bold(`${Colors.cyan}╚══════════════════════════════════════╝${Colors.reset}`))
    console.log(ansi.dim(`API: ${this.config.apiBase}`))
    console.log(ansi.dim('Type /help for commands, or enter a message to chat.\n'))

    // Subscribe to active task's SSE stream
    if (this.currentTaskId) {
      this.subscribeToTask(this.currentTaskId)
    }

    rl.prompt()

    rl.on('line', async (line: string) => {
      const input = line.trim()
      if (!input) {
        rl.prompt()
        return
      }

      if (input.startsWith('/')) {
        await this.handleCommand(input)
      } else {
        await this.handleMessage(input)
      }

      rl.prompt()
    })

    rl.on('close', () => {
      this.shutdown()
    })

    process.on('SIGINT', () => {
      if (this.isStreaming) {
        console.log(ansi.yellow('\n[Streaming interrupted]'))
        this.isStreaming = false
        rl.prompt()
      } else {
        this.shutdown()
      }
    })
  }

  private async handleCommand(input: string): Promise<void> {
    const [cmd, ...args] = input.slice(1).split(/\s+/)
    switch (cmd.toLowerCase()) {
      case 'help':
        this.printHelp()
        break

      case 'tasks':
        await this.cmdTasks()
        break

      case 'task':
        await this.cmdTask(args[0])
        break

      case 'credential':
        await this.cmdCredential(args)
        break

      case 'history':
        await this.cmdHistory(args[0])
        break

      case 'status':
        await this.cmdStatus(args[0])
        break

      case 'quit':
      case 'exit':
        this.shutdown()
        break

      default:
        console.log(ansi.red(`Unknown command: /${cmd}`))
        console.log(ansi.dim('Type /help for available commands'))
    }
  }

  private async handleMessage(content: string): Promise<void> {
    const taskId = this.currentTaskId ?? this.store.getActiveTaskId()

    if (!taskId) {
      console.log(ansi.red('No active task. Use /task <id> or /tasks to select one.'))
      return
    }

    this.isStreaming = true
    console.log(ansi.userMessage(content))
    console.log(ansi.dim('  Awaiting response...\n'))

    let fullResponse = ''
    try {
      const history = this.store.getConversationHistory(taskId)

      for await (const chunk of streamChat(
        this.config.apiBase,
        taskId,
        content,
        history,
      )) {
        // Print chunk inline (streaming)
        process.stdout.write(chunk)
        fullResponse += chunk
      }
      console.log('\n')

      // Save to session
      this.store.addMessage(taskId, 'user', content)
      this.store.addMessage(taskId, 'assistant', fullResponse)
      this.isStreaming = false
    } catch (err) {
      console.log(ansi.red(`\nError: ${String(err)}`))
      this.isStreaming = false
    }
  }

  private printHelp(): void {
    console.log(`
${ansi.bold('Commands:')}
  /tasks              List all tasks
  /task <id>          Switch to a task (or create new if not found)
  /credential <id> <value>  Submit a credential for a pending request
  /history [taskId]   Show conversation history
  /status [taskId]    Show task status
  /quit, /exit        Exit the CLI

${ansi.bold('Tips:')}
  - Messages without a / prefix are sent to the active task
  - Use Tab for command completion
  - Credential values are transmitted through an isolated channel
`)
  }

  private async cmdTasks(): Promise<void> {
    try {
      const tasks = await fetchTasks(this.config.apiBase) as any[]
      if (!tasks.length) {
        console.log(ansi.dim('No tasks found.'))
        return
      }

      console.log(ansi.bold('\nTasks:'))
      console.log('─'.repeat(60))
      for (const task of tasks) {
        const marker = task.id === this.currentTaskId ? '→' : ' '
        const statusColor = task.status === 'working' ? ansi.statusWorking :
                            task.status === 'completed' ? ansi.statusDone : ansi.statusAwaiting
        console.log(`  ${marker} [${statusColor(task.status)}] ${task.id}  ${task.title ?? 'Untitled'}`)
        if (task.lastUpdate) {
          console.log(ansi.dim(`       ${task.lastUpdate}`))
        }
      }
      console.log()
    } catch (err) {
      console.log(ansi.red(`Failed to fetch tasks: ${String(err)}`))
    }
  }

  private async cmdTask(taskId?: string): Promise<void> {
    if (!taskId) {
      if (this.currentTaskId) {
        console.log(`Active task: ${this.currentTaskId}`)
      } else {
        console.log(ansi.dim('No active task.'))
      }
      return
    }

    // Unsubscribe from old task
    const oldClient = this.sseClients.get(this.currentTaskId ?? '')
    if (oldClient) {
      oldClient.disconnect()
      this.sseClients.delete(this.currentTaskId ?? '')
    }

    this.currentTaskId = taskId
    this.store.setActiveTask(taskId)

    // Subscribe to new task
    this.subscribeToTask(taskId)
    console.log(ansi.green(`Switched to task: ${taskId}`))

    // Print conversation history
    await this.cmdHistory(taskId)
  }

  private async cmdCredential(args: string[]): Promise<void> {
    if (args.length < 1) {
      if (!this.pendingCredentials.size) {
        console.log(ansi.dim('No pending credential requests.'))
        return
      }
      console.log(ansi.bold('\nPending credential requests:'))
      for (const [id, req] of this.pendingCredentials) {
        console.log(`  ${ansi.cyan(id)}  ${req.label} (${req.keyName})`)
      }
      console.log(ansi.dim('\nUsage: /credential <requestId> <value>'))
      return
    }

    if (args.length < 2) {
      console.log(ansi.red('Usage: /credential <requestId> <value>'))
      return
    }

    const [requestId, value] = args
    const credReq = this.pendingCredentials.get(requestId)

    if (!credReq) {
      console.log(ansi.red(`Unknown credential request: ${requestId}`))
      return
    }

    console.log(ansi.credentialRequest(`Submitting credential for "${credReq.label}"...`))

    const result = await submitCredential(this.config.apiBase, {
      taskId: credReq.taskId,
      requestId: credReq.requestId,
      userId: credReq.userId,
      keyName: credReq.keyName,
      value,
    })

    if (result.ok) {
      console.log(ansi.green('✓ Credential stored securely.'))
      this.pendingCredentials.delete(requestId)
    } else {
      console.log(ansi.red(`✗ Failed: ${result.error}`))
    }
  }

  private async cmdHistory(taskId?: string): Promise<void> {
    const id = taskId ?? this.currentTaskId
    if (!id) {
      console.log(ansi.red('No task specified and no active task.'))
      return
    }

    try {
      const logs = await fetchTaskLogs(this.config.apiBase, id) as any[]
      if (!logs.length) {
        console.log(ansi.dim(`No history for task ${id}.`))
        return
      }

      console.log(ansi.bold(`\nConversation history (${id}):`))
      console.log('─'.repeat(60))
      for (const log of logs) {
        const roleLabel = log.role === 'user' ? 'you' : 'cerberOS'
        const prefix = log.role === 'user' ? '  ' : '  '
        console.log(prefix + ansi.dim(`[${roleLabel}]`))
        // Wrap long messages
        const lines = log.content.match(/.{1,72}/g) ?? [log.content]
        for (const line of lines) {
          console.log(prefix + '  ' + line)
        }
        console.log(prefix + ansi.dim(`  at ${new Date(log.at).toLocaleTimeString()}`))
        console.log()
      }
    } catch (err) {
      console.log(ansi.red(`Failed to fetch history: ${String(err)}`))
    }
  }

  private async cmdStatus(taskId?: string): Promise<void> {
    const id = taskId ?? this.currentTaskId
    if (!id) {
      console.log(ansi.red('No task specified and no active task.'))
      return
    }

    const task = this.store.getTask(id)
    if (!task) {
      console.log(ansi.red(`Task not found: ${id}`))
      return
    }

    const statusColor = task.status === 'working' ? ansi.statusWorking :
                        task.status === 'completed' ? ansi.statusDone : ansi.statusAwaiting
    console.log(`
  Task:    ${ansi.bold(task.title ?? id)}
  Status:  ${statusColor(task.status)}
  Update:  ${task.lastUpdate ?? '—'}
  ETA:     ${task.expectedNextInput ?? '—'}
`)
  }

  private subscribeToTask(taskId: string): void {
    const unsub = subscribeToTask(taskId, this.config.apiBase, {
      onStatusUpdate: (update: StatusUpdate) => {
        this.store.updateTaskStatus(update)
        if (update.taskId === this.currentTaskId) {
          // Print inline status update
          clearLine()
          console.log(ansi.statusWorking(`[${update.status}] ${update.lastUpdate}`))
          if (update.status === 'awaiting_feedback') {
            console.log(ansi.cyan('  → Your input is needed.'))
          }
        }
      },
      onCredentialRequest: (req: CredentialRequest) => {
        this.pendingCredentials.set(req.requestId, req)
        clearLine()
        console.log(ansi.credentialRequest(
          `Credential required: "${req.label}" — use /credential ${req.requestId} <value>`
        ))
      },
      onError: (err: Error) => {
        console.log(ansi.red(`[SSE error] ${err.message}`))
      },
    })

    this.sseClients.set(taskId, { disconnect: unsub })
  }

  private shutdown(): void {
    console.log(ansi.dim('\nShutting down...'))
    for (const client of this.sseClients.values()) {
      try { client.disconnect() } catch { /* ignore */ }
    }
    rl.close()
    process.exit(0)
  }
}

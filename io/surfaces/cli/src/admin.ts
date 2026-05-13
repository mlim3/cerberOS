/**
 * Admin subcommands for the cerberOS CLI.
 *
 * Usage:
 *   cerberos init --email=alice@example.com
 *   cerberos admin add-user --email=bob@example.com --role=user
 *   cerberos admin list-users
 *   cerberos admin set-llm-key --provider=anthropic --key=sk-...
 *   cerberos admin set-gmail --email=demo@gmail.com --app-password="xxxx xxxx xxxx xxxx"
 *   cerberos admin gmail-status
 *   cerberos skill create "<description>"
 *   cerberos skill import github.com/user/repo [--all-users]
 *     (imports skill-like files from the repo and publishes matching skills)
 *
 * All admin commands hit the IO API. They use CLI_USER_ID as the active user
 * (so the `requireRole` middleware can enforce role >= manager). Honor-system
 * identity, same as the web UI.
 */

import { config } from './config'

interface ParsedArgs {
  positional: string[]
  flags: Record<string, string | true>
}

function parseArgs(argv: string[]): ParsedArgs {
  const positional: string[] = []
  const flags: Record<string, string | true> = {}
  for (const arg of argv) {
    if (arg.startsWith('--')) {
      const eq = arg.indexOf('=')
      if (eq > 0) {
        flags[arg.slice(2, eq)] = arg.slice(eq + 1)
      } else {
        flags[arg.slice(2)] = true
      }
    } else {
      positional.push(arg)
    }
  }
  return { positional, flags }
}

function activeHeaders(): Record<string, string> {
  return {
    'Content-Type': 'application/json',
    'X-Active-User': config.userId,
  }
}

async function jsonOrText(res: Response): Promise<unknown> {
  const ct = res.headers.get('content-type') ?? ''
  if (ct.includes('application/json')) return res.json()
  return res.text()
}

async function cmdInit(flags: Record<string, string | true>): Promise<number> {
  const email = typeof flags.email === 'string' ? flags.email : ''
  if (!email) {
    console.error('cerberos init requires --email=<address>')
    return 2
  }
  const res = await fetch(`${config.apiBase}/api/users`, {
    method: 'POST',
    headers: activeHeaders(),
    body: JSON.stringify({
      email,
      role: 'root',
      id: '00000000-0000-0000-0000-000000000001',
    }),
  })
  const body = await jsonOrText(res)
  if (!res.ok) {
    console.error('init failed:', body)
    return 1
  }
  console.log(JSON.stringify(body, null, 2))
  return 0
}

async function cmdAdminAddUser(flags: Record<string, string | true>): Promise<number> {
  const email = typeof flags.email === 'string' ? flags.email : ''
  const role = typeof flags.role === 'string' ? flags.role : 'user'
  if (!email) {
    console.error('admin add-user requires --email=<address>')
    return 2
  }
  const res = await fetch(`${config.apiBase}/api/users`, {
    method: 'POST',
    headers: activeHeaders(),
    body: JSON.stringify({ email, role }),
  })
  const body = await jsonOrText(res)
  if (!res.ok) {
    console.error('add-user failed:', body)
    return 1
  }
  console.log(JSON.stringify(body, null, 2))
  return 0
}

async function cmdAdminListUsers(): Promise<number> {
  const res = await fetch(`${config.apiBase}/api/users`)
  const body = await jsonOrText(res) as { users?: Array<{ email: string; role?: string; id: string }>; root_count?: number }
  if (!res.ok) {
    console.error('list-users failed:', body)
    return 1
  }
  for (const u of body.users ?? []) {
    console.log(`${u.role ?? 'user'}\t${u.email}\t${u.id}`)
  }
  console.log(`# root_count=${body.root_count ?? 0}`)
  return 0
}

async function cmdAdminSetLLMKey(flags: Record<string, string | true>): Promise<number> {
  const provider = typeof flags.provider === 'string' ? flags.provider.toLowerCase() : ''
  const key = typeof flags.key === 'string' ? flags.key : ''
  if (!provider || !key) {
    console.error('admin set-llm-key requires --provider=<anthropic|openai> --key=<value>')
    return 2
  }
  if (provider !== 'anthropic' && provider !== 'openai') {
    console.error(`unknown provider: ${provider}`)
    return 2
  }
  const res = await fetch(`${config.apiBase}/api/admin/llm-keys`, {
    method: 'POST',
    headers: activeHeaders(),
    body: JSON.stringify({ provider, key }),
  })
  const body = await jsonOrText(res)
  if (!res.ok) {
    console.error('set-llm-key failed:', body)
    return 1
  }
  console.log('ok')
  return 0
}

async function cmdAdminSetGmail(flags: Record<string, string | true>): Promise<number> {
  const email = typeof flags.email === 'string' ? flags.email : ''
  const rawPassword = typeof flags['app-password'] === 'string' ? flags['app-password'] : ''
  if (!email || !rawPassword) {
    console.error('admin set-gmail requires --email=<address> --app-password="<16-char app password>"')
    console.error('Generate the app password at https://myaccount.google.com/apppasswords (needs 2FA enabled).')
    return 2
  }
  const cleaned = rawPassword.replace(/\s+/g, '')
  if (cleaned.length !== 16) {
    console.error(`app password must be 16 characters; got ${cleaned.length}. Did you paste your account password by mistake?`)
    return 2
  }
  const res = await fetch(`${config.apiBase}/api/admin/gmail-credentials`, {
    method: 'POST',
    headers: activeHeaders(),
    body: JSON.stringify({ email, app_password: cleaned }),
  })
  const body = await jsonOrText(res)
  if (!res.ok) {
    console.error('set-gmail failed:', body)
    return 1
  }
  console.log('ok — gmail_send and calendar_create_event are live (no restart needed)')
  return 0
}

async function cmdAdminGmailStatus(): Promise<number> {
  const res = await fetch(`${config.apiBase}/api/admin/gmail-credentials`, {
    headers: activeHeaders(),
  })
  const body = await jsonOrText(res) as { configured?: boolean; email?: string | null; error?: string }
  if (!res.ok) {
    console.error('gmail-status failed:', body)
    return 1
  }
  if (body.configured && body.email) {
    console.log(`configured: ${body.email}`)
  } else {
    console.log('not configured — run: cerberos admin set-gmail --email=... --app-password=...')
  }
  return 0
}

async function cmdSkillCreate(positional: string[], flags: Record<string, string | true>): Promise<number> {
  const description = positional.join(' ').trim() || (typeof flags.description === 'string' ? flags.description : '')
  if (!description) {
    console.error('skill create requires a description: cerberos skill create "..."')
    return 2
  }
  const scope = flags.all === true ? 'all' : 'me'
  const res = await fetch(`${config.apiBase}/api/skills/create`, {
    method: 'POST',
    headers: activeHeaders(),
    body: JSON.stringify({ description, scope }),
  })
  const body = await jsonOrText(res)
  if (!res.ok) {
    console.error('skill create failed:', body)
    return 1
  }
  console.log(JSON.stringify(body, null, 2))
  return 0
}

async function cmdSkillImport(positional: string[], flags: Record<string, string | true>): Promise<number> {
  const repo = positional[0] ?? ''
  if (!repo) {
    console.error('skill import requires <repo>: cerberos skill import github.com/user/repo')
    return 2
  }
  const scope = flags['all-users'] === true ? 'all' : 'me'
  const res = await fetch(`${config.apiBase}/api/skills/import-github`, {
    method: 'POST',
    headers: activeHeaders(),
    body: JSON.stringify({ repo, scope }),
  })
  const body = await jsonOrText(res)
  if (!res.ok) {
    console.error('skill import failed:', body)
    return 1
  }
  console.log(JSON.stringify(body, null, 2))
  return 0
}

export async function dispatchAdmin(argv: string[]): Promise<number> {
  const [head, sub, ...rest] = argv
  const parsed = parseArgs(rest)

  if (head === 'init') {
    return cmdInit(parsed.flags)
  }

  if (head === 'admin') {
    switch (sub) {
      case 'add-user':
        return cmdAdminAddUser(parsed.flags)
      case 'list-users':
        return cmdAdminListUsers()
      case 'set-llm-key':
        return cmdAdminSetLLMKey(parsed.flags)
      case 'set-gmail':
        return cmdAdminSetGmail(parsed.flags)
      case 'gmail-status':
        return cmdAdminGmailStatus()
      default:
        console.error(`unknown admin subcommand: ${sub ?? '(none)'}`)
        console.error('available: add-user, list-users, set-llm-key, set-gmail, gmail-status')
        return 2
    }
  }

  if (head === 'skill') {
    switch (sub) {
      case 'create':
        return cmdSkillCreate(parsed.positional, parsed.flags)
      case 'import':
        return cmdSkillImport(parsed.positional, parsed.flags)
      default:
        console.error(`unknown skill subcommand: ${sub ?? '(none)'}`)
        console.error('available: create, import')
        return 2
    }
  }

  console.error(`unknown command: ${head}`)
  return 2
}

export function isAdminCommand(argv: string[]): boolean {
  const head = argv[0]
  return head === 'init' || head === 'admin' || head === 'skill'
}

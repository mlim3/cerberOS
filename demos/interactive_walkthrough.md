# Interactive Walkthrough: Aegis Agents Component

This document outlines how to run the `aegis-agents` component in interactive mode and verify its core functionality using the built-in CLI.

## Prerequisites

Ensure you are in the project root:
```bash
cd /Users/chemch/GolandProjects/cerberOS
```

## 1. Start the Application

Run the application with the required environment variables. The `AEGIS_NATS_URL` is required by config but not used by the stub client in this mode.

**Important:** You must run the command from inside the `agents-component` directory so that Go can find the `go.mod` file.

```bash
cd agents-component
export AEGIS_NATS_URL="nats://localhost:4222"
export AEGIS_COMPONENT_ID="aegis-agents-local"
go run cmd/aegis-agents/main.go
```

You should see startup logs followed by:
```
--- Interactive Mode ---
Commands: task <id> <skill>, query <skill>, list, exit
>
```

## 2. Provision a New Agent (Task Assignment)

Simulate the Orchestrator sending a task. Since no agents exist yet, this triggers the full provisioning flow (Factory → Credentials → Lifecycle → Registry).

**Command:**
```text
> task task-1 web
```

**Expected Output:**
- Logs indicating `task_spec received`.
- Status updates showing the agent moving through states:
  ```
  [STATUS] Agent: agent-<timestamp> | Task: task-1 | State: provisioned
  [STATUS] Agent: agent-<timestamp> | Task: task-1 | State: assigned
  ```

## 3. Verify Agent State

Check the internal registry to confirm the agent was created and is currently active.

**Command:**
```text
> list
```

**Expected Output:**
```
Registered Agents: 1
- agent-<timestamp> [active] Skills: [web]
```

## 4. Query Capabilities

Simulate the Orchestrator asking if an agent with specific skills exists.

**Command:**
```text
> query web
```

**Expected Output:**
```
[QUERY RESULT] Match: true
```

If you query for a skill that no active agent possesses (or hasn't been provisioned yet):

**Command:**
```text
> query storage
```

**Expected Output:**
```
[QUERY RESULT] Match: false
```

## 5. Provision a Second Agent

Since the first agent is busy (`active`) with `task-1`, requesting another task requiring the same skill will force the Factory to provision a *new* agent rather than reusing the existing one.

**Command:**
```text
> task task-2 web
```

**Expected Output:**
- A new agent ID is generated.
- Status updates for the new agent.
- `list` will now show 2 agents.

## 6. Exit

Cleanly shut down the application.

**Command:**
```text
> exit
```

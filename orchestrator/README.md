## How to test orchetrator with agent component

Start agent
```bash
cd agents-component
docker compose up --build
```

Start orchestrator
```bash
cd orchestrator
NATS_URL=nats://localhost:4222 NODE_ID=orch-dev go run ./cmd/orchestrator
```

Subscribe `aegis.agents.task.inbound`
```bash
nats sub "aegis.agents.task.inbound" --server nats://localhost:4222
```

Subscribe `aegis.orchestrator.task.result`
```bash
nats sub "aegis.orchestrator.task.result" --server nats://localhost:4222
```

Orchestrator sends a user input task
```bash
cd cerberOS
nats pub aegis.orchestrator.tasks.inbound --server nats://localhost:4222 < orchestrator/testdata/nats/user_task.json
```
It can be observed that the agent planner function executed successfully

the task was decomposed into three sub-tasks with the following IDs:
 - `gather-ingredients`
 - `prepare-components`
 - `assemble-sandwich`

Dependencies:
 - `gather-ingredients`: No dependencies
 - `prepare-components`: Depends on `gather-ingredients`
 - `assemble-sandwich`: Depends on `prepare-components`

The orchestrator proceeded to dispatch the sub-tasks and subsequently received the results from sub-tasks
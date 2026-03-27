# Refactor plans

## Vault current form

- Vault accepts a script with placeholders for secrets and executes on the agent's behalf. This security model ensures the agent NEVER sees secrets.
- **PROBLEM 1**: This approach removes any sense of flexibility by turning Vault effectively into the agent executor. Agent needs to be able to do what it needs to do in it's own env. So execution model needs to shift the heavy lifting to the agent.
- **PROPOSAL 1**: Remove Vault's `engine` sandbox enviroment and keep Vault solely as a credential broker that confirms an agent is allowed to access requested credentials. How does that look?

**V1**: Agent calls the cli `vault inject <script> <list of secrets>` -> vault runs a check that the agent has access to ALL of these secrets (must be atomic) -> afterwards vault injects the credentials into the script and passes it back to the agent.

Yes, this approach is currently insecure, but this is V1 refactor and we will build upon this so keep it flexible with abstractions.

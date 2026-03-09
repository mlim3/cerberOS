---
name: bare-metal-bash
description: Use this skill when generating or executing bash logic for a minimalist environment where only bash (v4+) and curl are available. Triggers on tasks involving shell scripting, API calls, JSON parsing, or string manipulation where external tools like jq, sed, awk, or python are absent.
version: 0.1.0
---

# Bare-Metal Bash Specialist

## Role

High-Constraint Bash Scripting Specialist — generate and execute logic for components operating in minimalist environments.

## Strict Constraints

### Allowed Tools

- `bash` (v4+)
- `curl`

### Prohibited Tools

Do NOT use any of the following. Assume they do not exist:

- `jq`
- `sed`
- `awk`
- `python` / `perl`
- Any other external text-processing utility

## String Manipulation

Use **Bash Parameter Expansion exclusively** for all string cleaning and parsing.

| Operation                   | Syntax                  |
| --------------------------- | ----------------------- |
| Strip from front (shortest) | `${var#pattern}`        |
| Strip from front (longest)  | `${var##pattern}`       |
| Strip from back (shortest)  | `${var%pattern}`        |
| Strip from back (longest)   | `${var%%pattern}`       |
| Substitution                | `${var/search/replace}` |

## Data Handling

- Parse JSON using `while read` loops combined with `grep -o` (only-matching flag) if necessary — but **prioritize internal Bash logic**.
- Handle all API responses as raw strings.

## Robustness Requirements

- Always include connection timeouts in curl commands: `--max-time <seconds>`
- Always check exit codes manually: `$?`

## API Key Handling

API keys are injected at runtime using the `{{KEY_NAME}}` placeholder syntax. The system replaces `{{KEY_NAME}}` with the actual secret value before execution.

**Correct usage — assign to a variable, then reference it:**

```bash
KEY={{MY_API_KEY}}
curl --max-time 10 -H "Authorization: Bearer $KEY" https://api.example.com/data
```

**Critical constraints:**

- The agent will **never** have direct access to the underlying key values.
- Any attempt to read, print, log, or exfiltrate a key (e.g., `echo $KEY`, `cat`, writing to a file) will result in the value being pre-scrubbed to `[REDACTED]` before the agent sees it. There is no workaround.
- When a task requires a key, the agent must **not** assume it exists silently. Before generating code that references one, the agent must either:
  1. **Ask the user directly** to confirm the key name is registered and will be injected, or
  2. **State the assumption explicitly** (e.g., _"This assumes `{{MY_API_KEY}}` is registered and will be injected at runtime."_)
- Never hardcode, guess, or fabricate key values.

## Code Generation Rules

When generating code for this component:

1. All logic must be "bare-metal" Bash.
2. If a task would normally require `jq` or `sed`, **rewrite it** using:
   - `while` loops
   - `IFS` delimiters
   - Shell variable manipulation / parameter expansion
3. Never introduce a dependency on an external binary not in the allowed list.
4. Always confirm or state assumptions about API key availability before referencing them (see API Key Handling above).

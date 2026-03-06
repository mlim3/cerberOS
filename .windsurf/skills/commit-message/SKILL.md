---
name: commit-message
description: Generate clear, consistent, and helpful Git commit messages using the Conventional Commits format.
---

**Objective:** Create clear, consistent, and helpful Git commit messages using the industry standard Conventional Commits format.

## Core Rules

* **Keep it imperative:** Write the subject line like you are giving a command (e.g., "Add user login" instead of "Added user login" or "Adds user login").
* **Keep it short:** Limit the first line to 50 characters or less.
* **Capitalize:** Start the subject line with a capital letter.
* **No periods:** Do not put a period at the end of the subject line.
* **Wrap the text:** If you include a body section, wrap the text at 72 characters so it is easy to read in terminal windows.
* **Focus on the "Why":** Use the body to explain the reasoning behind the code change and what exact problem it solves.

## Commit Types

Use these specific prefixes to categorize the work:

| Prefix | Purpose |
| :--- | :--- |
| **feat** | A new feature for the user. |
| **fix** | A bug fix. |
| **docs** | Changes to the documentation only. |
| **style** | Formatting updates that do not change how the code works (spacing, missing commas, etc). |
| **refactor** | Code changes that rewrite logic but neither fix a bug nor add a feature. |
| **test** | Adding missing tests or fixing existing ones. |
| **chore** | Routine tasks, maintenance, or build process updates. |

## Formatting Template

Fill out this exact structure:

<type>(<optional scope>): <Subject line under 50 characters>

<Optional body explaining the 'why', wrapped at 72 characters>

<Optional footer for issue tracking, e.g., Closes #123>

## Good vs. Bad Examples

* **Bad:** fixed the login bug
* **Good:** fix(auth): Resolve infinite loop on login page
* **Bad:** added new stuff to the homepage
* **Good:** feat(ui): Add hero banner to landing page
* **Bad:** code review changes
* **Good:** refactor(api): Simplify database query logic

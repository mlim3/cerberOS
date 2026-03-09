# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

cerberOS is a monorepo containing two main applications:

1. **site/** - A Next.js 16 website (deployed to GitHub Pages)
2. **io/** - A React + Vite "IO component" for AI agent task management

The io component is the primary codebase - it provides a user interface for interacting with AI agent orchestrators, featuring task management, real-time chat with streaming responses, and activity logging with semantic heartbeats.

## Commands

### Site (Next.js)
```bash
cd site
bun dev          # Start development server (localhost:3000)
bun run build    # Production build
bun run lint     # ESLint
```

### IO Component (Vite + React)
```bash
cd io
bun dev          # Start dev server with HMR
bun run build    # TypeScript check + production build
bun run preview  # Preview production build
```

## Architecture

### IO Component Structure (`io/src/`)
- **components/** - React components: TaskSidebar, ChatWindow, SettingsPanel, ActivityLog
- **api/orchestrator.ts** - API layer for communicating with the orchestrator backend
- **lib/logging.ts** - Logging utilities

### Interface Contracts
The IO component implements interfaces defined in `io/io-interfaces.md`:
- **Status updates**: Semantic heartbeat from orchestrator (1-4s intervals) showing task status
- **Chat**: Streamed assistant responses with conversation history
- **Memory/Logging**: Log entries for user messages and orchestrator responses

### Deployment
- GitHub Actions workflow (`.github/workflows/site-deploy.yaml`) builds and deploys `site/` to GitHub Pages
- Triggers on push to main or PRs modifying site files

## Tech Stack
- **io**: React 19, TypeScript, Vite 7
- **site**: Next.js 16, React 19, TypeScript
- **Runtime**: Bun (preferred package manager)

## Context

Claude's context files are stored in `context/` at the project root.

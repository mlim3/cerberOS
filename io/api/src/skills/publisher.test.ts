import { afterEach, beforeEach, describe, expect, test } from 'bun:test'
import { importSkillsFromRepo } from './publisher'

const originalFetch = globalThis.fetch

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init.headers ?? {}),
    },
  })
}

function textResponse(body: string, init: ResponseInit = {}): Response {
  return new Response(body, init)
}

describe('skills publisher repo import', () => {
  beforeEach(() => {
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)

      if (url === 'https://api.github.com/repos/obra/superpowers') {
        return jsonResponse({ default_branch: 'main' })
      }

      if (url === 'https://api.github.com/repos/obra/superpowers/git/trees/main?recursive=1') {
        return jsonResponse({
          tree: [
            { path: 'skills/using-superpowers/SKILL.md', type: 'blob' },
            { path: 'skills/writing-skills.md', type: 'blob' },
            { path: 'docs/README.md', type: 'blob' },
            { path: 'scripts/helper.sh', type: 'blob' },
          ],
        })
      }

      if (url === 'https://raw.githubusercontent.com/obra/superpowers/main/skills/using-superpowers/SKILL.md') {
        return textResponse(`---
name: using-superpowers
description: Use when starting any conversation
---

# Using Superpowers

## Overview
This is the main activation skill.
`)
      }

      if (url === 'https://raw.githubusercontent.com/obra/superpowers/main/skills/writing-skills.md') {
        return textResponse(`---
name: writing-skills
description: Use when creating new skills
when_to_use: when editing or verifying skills
---

# Writing Skills

## Overview
Write and test skills.
`)
      }

      if (url === 'https://raw.githubusercontent.com/obra/superpowers/main/docs/README.md') {
        return textResponse(`# Project README\n\nThis is a normal project document.`)
      }

      if (url === 'https://raw.githubusercontent.com/obra/superpowers/main/scripts/helper.sh') {
        return textResponse('#!/bin/sh\necho helper')
      }

      if (url === 'https://api.github.com/repos/obra/superpowers/readme') {
        return textResponse('# fallback readme')
      }

      return new Response('not found', { status: 404 })
    }) as typeof fetch
  })

  afterEach(() => {
    globalThis.fetch = originalFetch
  })

  test('imports skill-like files even when they are not named SKILL.md', async () => {
    const result = await importSkillsFromRepo('github.com/obra/superpowers')

    expect(result.fallbackUsed).toBe(false)
    expect(result.skills.map((s) => s.path)).toEqual([
      'skills/using-superpowers/SKILL.md',
      'skills/writing-skills.md',
    ])
    expect(result.skills[0]?.node.name).toBe('using_superpowers')
    expect(result.skills[1]?.node.name).toBe('writing_skills')
    expect(result.skills[1]?.node.description).toContain('Use when creating new skills')
  })

  test('falls back when the repo has no skill-like docs', async () => {
    globalThis.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === 'https://api.github.com/repos/acme/plain-repo') {
        return jsonResponse({ default_branch: 'main' })
      }
      if (url === 'https://api.github.com/repos/acme/plain-repo/git/trees/main?recursive=1') {
        return jsonResponse({
          tree: [
            { path: 'README.md', type: 'blob' },
            { path: 'docs/guide.md', type: 'blob' },
          ],
        })
      }
      if (url === 'https://raw.githubusercontent.com/acme/plain-repo/main/README.md') {
        return textResponse(`# Plain Repo\n\nJust documentation.`)
      }
      if (url === 'https://raw.githubusercontent.com/acme/plain-repo/main/docs/guide.md') {
        return textResponse(`# Guide\n\nMore docs.`)
      }
      if (url === 'https://api.github.com/repos/acme/plain-repo/readme') {
        return textResponse('# fallback readme')
      }
      return new Response('not found', { status: 404 })
    }) as typeof fetch

    const result = await importSkillsFromRepo('github.com/acme/plain-repo')

    expect(result.fallbackUsed).toBe(true)
    expect(result.skills).toHaveLength(1)
    expect(result.skills[0]?.path).toBe('README.md')
    expect(result.skills[0]?.node.name.length).toBeGreaterThan(0)
  })
})

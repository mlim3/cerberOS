import { useState } from 'react'
import type { SkillCreated } from '@cerberos/io-core'
import './SkillCreatedCard.css'

interface SkillCreatedCardProps {
  skill: SkillCreated
  onDismiss: () => void
}

function domainLabel(domain: string): string {
  if (!domain) return ''
  return domain.charAt(0).toUpperCase() + domain.slice(1)
}

function modeLabel(mode: string): string {
  const lower = (mode ?? '').toLowerCase()
  if (lower === 'synthesized') return 'Synthesized'
  if (lower === 'vault') return 'Vault-backed'
  if (lower) return mode
  return 'Synthesized'
}

function scopeLabel(scope: string): string {
  return scope === 'global' ? 'Global' : 'Your account'
}

export default function SkillCreatedCard({ skill, onDismiss }: SkillCreatedCardProps) {
  const [recipeOpen, setRecipeOpen] = useState(false)
  const hasRecipe = (skill.recipe ?? '').trim().length > 0

  return (
    <div className="sc-card" role="region" aria-label="New skill created">

      {/* ── Header ─────────────────────────────────────── */}
      <div className="sc-header">
        <span className="sc-sparkle" aria-hidden>✦</span>
        <span className="sc-headline">I built a new skill for you</span>
        <button
          type="button"
          className="sc-dismiss"
          onClick={onDismiss}
          aria-label="Dismiss skill card"
        >
          ×
        </button>
      </div>

      {/* ── Body ───────────────────────────────────────── */}
      <div className="sc-body">
        <div className="sc-name-row">
          {skill.domain && (
            <span className="sc-domain-badge">{domainLabel(skill.domain)}</span>
          )}
          <span className="sc-name">
            {skill.label && skill.label !== skill.skillName
              ? skill.label
              : skill.skillName}
          </span>
        </div>

        {skill.skillName && skill.label && skill.label !== skill.skillName && (
          <code className="sc-id">{skill.skillName}</code>
        )}

        {skill.description && (
          <p className="sc-description">{skill.description}</p>
        )}

        <div className="sc-chips">
          <span className="sc-chip sc-chip--mode">{modeLabel(skill.mode)}</span>
          <span className="sc-chip-sep" aria-hidden>·</span>
          <span className="sc-chip sc-chip--scope">{scopeLabel(skill.scope)}</span>
        </div>
      </div>

      {/* ── Recipe (collapsible) ───────────────────────── */}
      {hasRecipe && (
        <div className="sc-recipe-section">
          <button
            type="button"
            className="sc-recipe-toggle"
            aria-expanded={recipeOpen}
            onClick={() => setRecipeOpen(v => !v)}
          >
            <span className="sc-chevron" aria-hidden />
            {recipeOpen ? 'Hide recipe' : 'Show recipe'}
          </button>
          {recipeOpen && (
            <pre className="sc-recipe-body">{skill.recipe}</pre>
          )}
        </div>
      )}
    </div>
  )
}

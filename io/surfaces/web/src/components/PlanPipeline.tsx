import { useState } from 'react'
import type { PlanStep } from '@cerberos/io-core'
import './PlanPipeline.css'

interface PlanPipelineProps {
  steps: PlanStep[]
  currentStepIndex: number
}

export default function PlanPipeline({ steps, currentStepIndex }: PlanPipelineProps) {
  const [expandedStep, setExpandedStep] = useState<string | null>(null)

  if (steps.length <= 1) {
    const step = steps[0]
    if (!step) return null
    return (
      <div className="pipeline-single">
        <span className="pipeline-single-dot"></span>
        <span className="pipeline-single-text">{step.label}</span>
      </div>
    )
  }

  return (
    <div className="plan-pipeline" role="list" aria-label="Plan execution progress">
      {steps.map((step, i) => {
        const zone = i < currentStepIndex ? 'completed'
          : i === currentStepIndex ? 'active'
          : i === currentStepIndex + 1 ? 'next'
          : 'future'
        const isExpanded = expandedStep === step.id
        const isClickable = zone === 'completed' && !!step.output
        const isLast = i === steps.length - 1

        return (
          <div
            key={step.id}
            className={`pipeline-step pipeline-step-${zone}`}
            role="listitem"
            aria-current={zone === 'active' ? 'step' : undefined}
          >
            <div className="pipeline-rail">
              <div className={`pipeline-circle pipeline-circle-${zone}`}>
                {zone === 'completed' ? (
                  <span className="pipeline-check">{'\u2713'}</span>
                ) : zone === 'active' ? (
                  <span className="pipeline-active-dot"></span>
                ) : (
                  <span className="pipeline-number">{i + 1}</span>
                )}
              </div>
              {!isLast && (
                <div className={`pipeline-line pipeline-line-${zone}`}></div>
              )}
            </div>
            <div className="pipeline-content">
              <div
                className={`pipeline-label ${isClickable ? 'clickable' : ''}`}
                onClick={() => isClickable && setExpandedStep(isExpanded ? null : step.id)}
                role={isClickable ? 'button' : undefined}
                tabIndex={isClickable ? 0 : undefined}
                onKeyDown={(e) => {
                  if (isClickable && (e.key === 'Enter' || e.key === ' ')) {
                    e.preventDefault()
                    setExpandedStep(isExpanded ? null : step.id)
                  }
                }}
              >
                {step.label}
              </div>
              {zone === 'active' && step.description && (
                <div className="pipeline-active-status">
                  <span className="pipeline-status-dot"></span>
                  <span className="pipeline-status-text">{step.description}</span>
                </div>
              )}
              {isExpanded && step.output && (
                <div className="pipeline-output">{step.output}</div>
              )}
            </div>
          </div>
        )
      })}
    </div>
  )
}

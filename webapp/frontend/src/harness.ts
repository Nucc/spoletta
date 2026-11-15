// Which coding agent runs an agent process, its harness: pi, as every agent
// did before, or Claude Code, which the worker offers only when it has it
// installed (Models.harnesses). A process names it in its settings, absent
// for pi; a run's step says which ran it. Claude Code takes only models of
// providers speaking Anthropic's API (Models.claude), and an effort level
// rather than pi's thinking levels, of which the worker runs off and minimal
// as low. Where a process or step names its model, it names Claude Code
// beside it, and pi as before not at all. scripts/models.ts checks it.

import type { Models, ProcessSettings, ThinkingLevel } from './api'

// What a process's or step's harness is: 'claude' for Claude Code, else ''
// for pi, as one set to pi by name is too.
export function harnessOf(harness: string | undefined): '' | 'claude' {
  return harness === 'claude' ? 'claude' : ''
}

// A harness as people read it.
export function harnessName(harness: string | undefined): string {
  return harnessOf(harness) === 'claude' ? 'Claude Code' : 'pi'
}

// What names a model, with Claude Code said before it for a process or step
// it runs: Claude Code · anthropic/claude-sonnet-4-6.
export function withHarness(harness: string | undefined, text: string): string {
  return harnessOf(harness) === 'claude' ? `Claude Code · ${text}` : text
}

// Whether the worker offers Claude Code.
export const offersClaude = (models: Models | null) => models?.harnesses?.includes('claude') ?? false

// The models Claude Code may use.
export const claudeModels = (models: Models | null) => models?.claude ?? []

// Whether Claude Code may use a model, the default for none.
export function claudeCanUse(models: Models | null, model: string): boolean {
  const m = model === '' ? (models?.default ?? '') : model
  return m !== '' && claudeModels(models).includes(m)
}

// The effort levels Claude Code takes, in the order its picker lists them.
export const CLAUDE_EFFORTS: ThinkingLevel[] = ['low', 'medium', 'high', 'xhigh', 'max']

// The effort Claude Code runs at for a thinking level: off and minimal as
// low, the default as none.
export function claudeEffort(thinking: ThinkingLevel | ''): ThinkingLevel | '' {
  return thinking === 'off' || thinking === 'minimal' ? 'low' : thinking
}

// A process's settings, model and thinking once switched to a harness:
// Claude Code drops a model it can't use, so the process takes the
// default, and thinks at the effort it would run at; pi keeps them as they
// are. While the models load, the model is kept.
export function switchHarness(
  to: '' | 'claude',
  current: { settings: ProcessSettings; model: string; thinking: ThinkingLevel | '' },
  models: Models | null,
): { settings: ProcessSettings; model: string; thinking: ThinkingLevel | '' } {
  const { harness: _, ...rest } = current.settings
  if (to === '') return { ...current, settings: rest }
  const keep = current.model === '' || models === null || claudeModels(models).includes(current.model)
  return {
    settings: { ...rest, harness: 'claude' },
    model: keep ? current.model : '',
    thinking: claudeEffort(current.thinking),
  }
}

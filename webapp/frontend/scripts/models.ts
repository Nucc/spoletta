// What the model picker makes of models' IDs (src/models.ts): their short
// names, and which are listed apart as not for an agent's conversation; and
// what the coding agent an agent runs with changes of its model and
// thinking (src/harness.ts). Run with `npm test`, which needs Node 22.6 or
// later to load TypeScript.

import type { Models } from '../src/api.ts'
import { claudeCanUse, claudeEffort, harnessName, offersClaude, switchHarness, withHarness } from '../src/harness.ts'
import { otherModel, shortModel } from '../src/models.ts'

declare const process: { exitCode?: number }

let failed = 0
let checked = 0
function check(what: string, got: unknown, want: unknown) {
  checked++
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++
    console.error(`${what}\n  got  ${JSON.stringify(got)}\n  want ${JSON.stringify(want)}`)
  }
}

check(
  'short names',
  [
    'openai/gpt-5.6-luna',
    'fireworks/accounts/fireworks/models/glm-5p3-flash',
    'amazon-bedrock/global.anthropic.claude-opus-5',
    'gateway/ft:gpt-4o-mini-2024-07-18:acme:support-tickets:Ab12Cd34',
    'gateway/router/auto',
    'gpt-5',
  ].map(shortModel),
  ['gpt-5.6-luna', 'glm-5p3-flash', 'global.anthropic.claude-opus-5', 'ft:gpt-4o-mini-2024-07-18:acme:support-tickets:Ab12Cd34', 'auto', 'gpt-5'],
)

const others = [
  'gateway/gpt-4o-mini-tts',
  'gateway/tts-1-hd',
  'gateway/gpt-4o-transcribe-diarize',
  'gateway/whisper-1',
  'gateway/gpt-audio-mini-2025-10-06',
  'openai/gpt-realtime-2.1',
  'gateway/gpt-live-1',
  'gateway/gpt-image-2.5-flare',
  'gateway/chatgpt-image-latest',
  'gateway/dall-e-3',
  'gateway/sora-2-pro',
  'gateway/text-embedding-3-large',
  'gateway/accounts/fireworks/models/qwen3-embedding-8b',
  'gateway/accounts/fireworks/models/qwen3-reranker-8b',
  'gateway/omni-moderation-latest',
  'gateway/gpt-3.5-turbo-instruct-0914',
  'gateway/computer-use-preview',
  'gateway/davinci-002',
  'gateway/babbage-002',
  'gateway/curie:ft-acme-2023-05-13-04-09-52',
  'gateway/ada:ft-acme-2023-05-13-03-55-04',
]
check('models for something else', others.filter((m) => !otherModel(m)), [])

const chat = [
  'openai/gpt-5.6-luna',
  'openai/gpt-5.3-codex',
  'gateway/gpt-4o-search-preview',
  'gateway/gpt-5-chat-latest',
  'gateway/chat-latest',
  'gateway/o3-deep-research',
  'gateway/gpt-5-search-api',
  'gateway/ft:gpt-3.5-turbo-1106:acme-dev:support-tickets:Ef56Gh78',
  'gateway/gpt-5-exp-0619-ab12cd-vis-1-treatment',
  'fireworks/accounts/fireworks/models/kimi-k3',
  'amazon-bedrock/global.anthropic.claude-haiku-4-5-20251001-v1:0',
  'gateway/alpha-pro-2025-12-09',
  'gateway/auto',
  'openai/xai.grok-4.6',
  'gateway/accounts/fireworks/models/spark-open',
  'gateway/accounts/fireworks/models/nemotron-lightning-3p5-30b-a3b',
]
check('chat models, and those unclear', chat.filter(otherModel), [])

// The coding agent an agent runs with.
{
  const models: Models = {
    default: 'anthropic/claude-sonnet-4-6',
    models: ['anthropic/claude-sonnet-4-6', 'anthropic/claude-opus-5', 'gateway/claude-haiku-4-5', 'openai/gpt-5.6-luna'],
    harnesses: ['pi', 'claude'],
    claude: ['anthropic/claude-sonnet-4-6', 'anthropic/claude-opus-5', 'gateway/claude-haiku-4-5'],
  }
  const piOnly: Models = { default: 'openai/gpt-5.6-luna', models: ['openai/gpt-5.6-luna'] }

  check('offered by the worker', [offersClaude(models), offersClaude(piOnly), offersClaude(null)], [true, false, false])
  check('named, pi by name or none', ['', 'pi', 'claude'].map(harnessName), ['pi', 'pi', 'Claude Code'])
  check(
    'named beside the model only for Claude Code',
    [withHarness('', 'anthropic/claude-sonnet-4-6'), withHarness('claude', 'anthropic/claude-sonnet-4-6')],
    ['anthropic/claude-sonnet-4-6', 'Claude Code · anthropic/claude-sonnet-4-6'],
  )
  check(
    'models Claude Code may use, the default for none',
    [
      claudeCanUse(models, 'anthropic/claude-opus-5'),
      claudeCanUse(models, 'openai/gpt-5.6-luna'),
      claudeCanUse(models, ''),
      claudeCanUse(piOnly, ''),
      claudeCanUse({ default: '', models: [], claude: [] }, ''),
    ],
    [true, false, true, false, false],
  )
  check('thinking as an effort', (['', 'off', 'minimal', 'low', 'high', 'max'] as const).map(claudeEffort), ['', 'low', 'low', 'low', 'high', 'max'])

  const agent = { settings: { skills: [1] }, model: 'openai/gpt-5.6-luna', thinking: 'minimal' as const }
  check('switching to Claude Code drops a model it can\'t use and thinks at its effort', switchHarness('claude', agent, models), {
    settings: { skills: [1], harness: 'claude' },
    model: '',
    thinking: 'low',
  })
  check(
    'and keeps one it can',
    switchHarness('claude', { settings: {}, model: 'gateway/claude-haiku-4-5', thinking: 'high' }, models),
    { settings: { harness: 'claude' }, model: 'gateway/claude-haiku-4-5', thinking: 'high' },
  )
  check('and the model while the models load', switchHarness('claude', agent, null).model, 'openai/gpt-5.6-luna')
  check(
    'switching back to pi leaves the rest as it is, the harness unset',
    switchHarness('', { settings: { harness: 'claude', skills: [1] }, model: '', thinking: 'low' }, models),
    { settings: { skills: [1] }, model: '', thinking: 'low' },
  )
}

console.log(`${checked} checks of the models' names and harnesses, ${failed} failed`)
if (failed > 0) process.exitCode = 1

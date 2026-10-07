// Runs pi agent harnesses on behalf of the webapp. Each POST /run spawns one
// non-interactive pi process in a fresh scratch directory and replies once it exits.
import { spawn } from 'node:child_process'
import { copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const PORT = Number(process.env.PORT ?? 8090)
const RUN_TIMEOUT_MS = Number(process.env.RUN_TIMEOUT_MS ?? 10 * 60 * 1000)
const MAX_BODY_BYTES = 1 << 20

// The user's pi config, mounted read-only. Only models.json and settings.json
// are used; credentials come from the environment.
const HOST_CONFIG_DIR = process.env.PI_HOST_CONFIG_DIR ?? '/pi-host'
// pi writes to its agent directory, so it gets a writable one built from the host config.
const AGENT_DIR = process.env.PI_CODING_AGENT_DIR ?? '/root/.pi/agent'

// Settings that choose models. The rest (packages, extensions, UI) belongs
// to the interactive setup and isn't wanted in headless runs.
const MODEL_SETTINGS = [
  'defaultProvider',
  'defaultModel',
  'defaultThinkingLevel',
  'modelThinkingLevels',
  'thinkingBudgets',
  'enabledModels',
]

const THINKING_SUFFIX = /:(off|minimal|low|medium|high|xhigh|max)$/

// enabledModels may hold patterns (anthropic/*, sonnet:high); only exact
// provider/id entries can be offered as choices. Ids like bedrock's ...-v1:0
// contain a colon too, so only thinking-level suffixes count as patterns.
function isExactModel(m) {
  return typeof m === 'string' && m.indexOf('/') > 0 && !/[*?]/.test(m) && !THINKING_SUFFIX.test(m)
}

async function readHostJSON(name) {
  try {
    return JSON.parse(await readFile(join(HOST_CONFIG_DIR, name), 'utf8'))
  } catch (err) {
    if (err.code === 'ENOENT') return null
    throw new Error(`${name}: ${err.message}`)
  }
}

// Builds the agent directory once at startup; restart the worker to pick up config changes.
async function prepareAgentDir() {
  await mkdir(AGENT_DIR, { recursive: true })
  const settings = (await readHostJSON('settings.json')) ?? {}
  const trimmed = Object.fromEntries(MODEL_SETTINGS.filter((k) => k in settings).map((k) => [k, settings[k]]))
  await writeFile(join(AGENT_DIR, 'settings.json'), JSON.stringify(trimmed, null, 2))
  if (await readHostJSON('models.json')) await copyFile(join(HOST_CONFIG_DIR, 'models.json'), join(AGENT_DIR, 'models.json'))
  else await rm(join(AGENT_DIR, 'models.json'), { force: true })

  const def = trimmed.defaultProvider && trimmed.defaultModel ? `${trimmed.defaultProvider}/${trimmed.defaultModel}` : ''
  return { default: def, models: (trimmed.enabledModels ?? []).filter(isExactModel) }
}

function readJSON(req) {
  return new Promise((resolve, reject) => {
    let body = ''
    req.setEncoding('utf8')
    req.on('data', (chunk) => {
      body += chunk
      if (body.length > MAX_BODY_BYTES) reject(new Error('request body too large'))
    })
    req.on('end', () => {
      try {
        resolve(JSON.parse(body))
      } catch (err) {
        reject(err)
      }
    })
    req.on('error', reject)
  })
}

// Logs the outcome followed by the agent's output (or error), each line
// prefixed so multi-line output stays attributable when runs interleave.
function logRun(run, result, ms) {
  const label = run.processId ? `process ${run.processId}${run.processName ? ` "${run.processName}"` : ''}` : 'run'
  console.log(`worker: ${label} with ${run.model || 'default model'} ${result.ok ? 'succeeded' : 'failed'} in ${ms}ms`)
  for (const [stream, text] of [['output', result.output], ['error', result.error]]) {
    for (const line of text ? text.split('\n') : []) console.log(`worker: ${label} ${stream}| ${line}`)
  }
}

function reply(res, status, body) {
  res.writeHead(status, { 'Content-Type': 'application/json' })
  res.end(JSON.stringify(body))
}

// Model ids may contain slashes and colons (fireworks/accounts/..., bedrock's
// ...-v1:0), so the provider is split off and passed separately rather than
// letting pi parse a provider/id[:thinking] pattern.
function modelArgs(model) {
  if (!model) return []
  const slash = model.indexOf('/')
  return slash > 0 ? ['--provider', model.slice(0, slash), '--model', model.slice(slash + 1)] : ['--model', model]
}

// Resolves with the run outcome; a client that disconnects or a run that
// exceeds the timeout kills pi.
async function runPi({ prompt, model, thinking }, res) {
  const cwd = await mkdtemp(join(tmpdir(), 'run-'))
  // An empty model or thinking level leaves the choice to the pi settings.
  const args = ['-p', '--no-session', '--no-approve', ...modelArgs(model)]
  if (thinking) args.push('--thinking', thinking)

  try {
    return await new Promise((resolve) => {
      // The prompt goes in on stdin: as an argument, a leading @ or - would be
      // parsed as a file reference or an option.
      const child = spawn('pi', args, { cwd, stdio: ['pipe', 'pipe', 'pipe'] })
      let stdout = ''
      let stderr = ''
      child.stdout.setEncoding('utf8').on('data', (d) => (stdout += d))
      child.stderr.setEncoding('utf8').on('data', (d) => (stderr += d))
      // pi may exit before reading its input, e.g. on a bad model; that's reported via close.
      child.stdin.on('error', () => {})
      child.stdin.end(prompt)

      let killedFor = null
      const kill = (reason) => {
        if (child.exitCode !== null || killedFor) return
        killedFor = reason
        child.kill('SIGTERM')
      }
      const timer = setTimeout(() => kill('timed out'), RUN_TIMEOUT_MS)
      res.on('close', () => kill('cancelled'))

      child.on('error', (err) => {
        clearTimeout(timer)
        resolve({ ok: false, output: stdout, error: `failed to start pi: ${err.message}` })
      })
      child.on('close', (code) => {
        clearTimeout(timer)
        if (code === 0 && !killedFor) resolve({ ok: true, output: stdout.trim(), error: '' })
        else resolve({ ok: false, output: stdout.trim(), error: killedFor ?? (stderr.trim() || `pi exited with code ${code}`) })
      })
    })
  } finally {
    await rm(cwd, { recursive: true, force: true })
  }
}

const models = await prepareAgentDir()
console.log(`worker: ${models.models.length} enabled models, default ${models.default || '(pi default)'}`)

createServer(async (req, res) => {
  if (req.method === 'GET' && req.url === '/health') return reply(res, 200, { ok: true })
  if (req.method === 'GET' && req.url === '/models') return reply(res, 200, models)
  if (req.method !== 'POST' || req.url !== '/run') return reply(res, 404, { error: 'not found' })

  let run
  try {
    run = await readJSON(req)
  } catch (err) {
    return reply(res, 400, { error: `invalid request: ${err.message}` })
  }
  if (typeof run.prompt !== 'string' || run.prompt === '') return reply(res, 400, { error: 'prompt is required' })
  for (const field of ['model', 'thinking']) {
    run[field] ??= ''
    if (typeof run[field] !== 'string' || run[field].startsWith('-')) return reply(res, 400, { error: `invalid ${field}` })
  }

  const started = Date.now()
  const result = await runPi(run, res)
  logRun(run, result, Date.now() - started)
  if (!res.writableEnded && !res.destroyed) reply(res, 200, result)
}).listen(PORT, () => console.log(`worker: listening on ${PORT}`))

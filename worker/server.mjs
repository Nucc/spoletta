// Runs pi agent harnesses on behalf of the webapp. Each POST /run spawns one
// non-interactive pi process in a fresh scratch directory and replies once it exits.
import { spawn } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const PORT = Number(process.env.PORT ?? 8090)
const RUN_TIMEOUT_MS = Number(process.env.RUN_TIMEOUT_MS ?? 10 * 60 * 1000)
const MAX_BODY_BYTES = 1 << 20

// Providers pi can authenticate with a plain API key, and the variable it reads it from.
const API_KEY_ENV = {
  anthropic: 'ANTHROPIC_API_KEY',
  openai: 'OPENAI_API_KEY',
  google: 'GEMINI_API_KEY',
  openrouter: 'OPENROUTER_API_KEY',
  mistral: 'MISTRAL_API_KEY',
  groq: 'GROQ_API_KEY',
  xai: 'XAI_API_KEY',
  deepseek: 'DEEPSEEK_API_KEY',
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

function reply(res, status, body) {
  res.writeHead(status, { 'Content-Type': 'application/json' })
  res.end(JSON.stringify(body))
}

// Resolves with the run outcome; a client that disconnects or a run that
// exceeds the timeout kills pi.
async function runPi({ prompt, provider, model, apiKey, thinking }, res) {
  const cwd = await mkdtemp(join(tmpdir(), 'run-'))
  const args = ['-p', '--no-session', '--no-approve', '--model', `${provider}/${model}`]
  if (thinking) args.push('--thinking', thinking)
  // The key goes through pi's provider variable rather than --api-key so it stays out of ps.
  const env = { ...process.env, [API_KEY_ENV[provider]]: apiKey }

  try {
    return await new Promise((resolve) => {
      // The prompt goes in on stdin: as an argument, a leading @ or - would be
      // parsed as a file reference or an option.
      const child = spawn('pi', args, { cwd, env, stdio: ['pipe', 'pipe', 'pipe'] })
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

createServer(async (req, res) => {
  if (req.method === 'GET' && req.url === '/health') return reply(res, 200, { ok: true })
  if (req.method !== 'POST' || req.url !== '/run') return reply(res, 404, { error: 'not found' })

  let run
  try {
    run = await readJSON(req)
  } catch (err) {
    return reply(res, 400, { error: `invalid request: ${err.message}` })
  }
  for (const field of ['prompt', 'provider', 'model', 'apiKey']) {
    if (typeof run[field] !== 'string' || run[field] === '') return reply(res, 400, { error: `${field} is required` })
  }
  if (!Object.hasOwn(API_KEY_ENV, run.provider)) return reply(res, 400, { error: `unsupported provider ${run.provider}` })

  const started = Date.now()
  const result = await runPi(run, res)
  console.log(`worker: run ${run.provider}/${run.model} ${result.ok ? 'succeeded' : 'failed'} in ${Date.now() - started}ms`)
  if (!res.writableEnded && !res.destroyed) reply(res, 200, result)
}).listen(PORT, () => console.log(`worker: listening on ${PORT}`))

import { createScreen } from './screen.js'

// The sandbox panel.
//
// Three rails: machines, their jobs, and one job's output. The output is a real Server-Sent Events
// stream from the worker's own `WatchJob` RPC, not a poll.
//
// Plain JS, no build step: the whole panel is this file plus one HTML document, both served by the
// extension.

/** Where this UI is mounted, derived so a rename of the Service cannot leave requests pointing at
 *  the old prefix. `/ext/sandbox` -> `/ext/sandbox/api/`. */
const API = (() => {
  const p = location.pathname.replace(/\/+$/, '')
  return `${p}/api/`
})()

function token() {
  try {
    return localStorage.getItem('agent.token') ?? ''
  } catch {
    return ''
  }
}

function authHeaders() {
  const t = token()
  return t ? { authorization: `Bearer ${t}` } : {}
}

/** The session this panel is scoped to, when the host supplied one. */
const SESSION = new URLSearchParams(location.search).get('session') ?? ''

/**
 * The panel's own hash route:
 *
 *   #/sb/<name>              one machine, its Screen
 *   #/sb/<name>/job/<id>     one machine, that job's output
 *
 * A tool's `view` builds these, so opening the panel from a result lands on the thing that result was
 * about instead of on an empty picker.
 */
const HASH = (() => {
  const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean)
  const out = { sandbox: '', job: '', view: 'screen' }
  if (parts[0] !== 'sb' || !parts[1]) return out
  out.sandbox = decodeURIComponent(parts[1])
  if (parts[2] === 'job' && parts[3]) {
    out.job = decodeURIComponent(parts[3])
    out.view = 'job'
  }
  return out
})()

function withSession(path) {
  if (SESSION === '') return path
  return path + (path.includes('?') ? '&' : '?') + 'session=' + encodeURIComponent(SESSION)
}

async function getJSON(path) {
  const res = await fetch(API + withSession(path), { headers: authHeaders() })
  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const b = await res.json()
      if (b && typeof b.error === 'string') msg = b.error
    } catch {
      /* keep the status */
    }
    throw new Error(msg)
  }
  return res.json()
}

// ---- state ----
let sandboxes = []
let jobs = []
let sbName = HASH.sandbox
let jobId = HASH.job
let listError = ''
let jobError = ''
let output = ''
/** The live EventSource, or null. Closed before a new one opens, so two do not interleave output. */
let es = null
let totalLines = 0
let shownLines = 0
/** The live screen player, or null when no stream is open. */
let screen = null
/** Which view the work area shows: the job stream or the machine's screen. */
let view = HASH.view
/** The player's own status line, so "connecting" never looks like "broken". */
let screenState = ''

// ---- loading ----
async function loadSandboxes() {
  try {
    const d = await getJSON('sandboxes')
    sandboxes = Array.isArray(d.sandboxes) ? d.sandboxes : []
    listError = ''
    // A sandbox that disappeared must not stay selected: the panel would keep offering a machine the
    // reader no longer has.
    if (sbName !== '' && !sandboxes.some(s => s.name === sbName)) {
      sbName = ''
      jobId = ''
      jobs = []
      stopStream()
    }
  } catch (e) {
    listError = e instanceof Error ? e.message : String(e)
  }
  render()
}

async function loadJobs() {
  if (sbName === '') return
  try {
    const d = await getJSON('jobs?sb=' + encodeURIComponent(sbName))
    jobs = Array.isArray(d.jobs) ? d.jobs : []
    jobError = ''
  } catch (e) {
    jobs = []
    jobError = e instanceof Error ? e.message : String(e)
  }
  render()
  // A job named in the hash is opened once its list arrives. Done HERE rather than at startup
  // because the job may be older than this page load: the panel asks the machine, and the machine
  // still has it.
  if (jobId !== '' && view === 'job' && output === '') {
    const j = jobs.find(x => x.id === jobId)
    if (j) watch(j)
  }
}

/**
 * Stream one job's output.
 *
 * REPLACES any existing stream rather than adding one: two EventSources on the same pane would
 * interleave their lines, and the second job would appear to be producing the first one's output.
 */
function watch(job) {
  stopStream()
  jobId = job.id
  output = ''
  totalLines = job.totalLines ?? 0
  shownLines = 0
  render()

  const url = API + withSession(`jobs/${encodeURIComponent(sbName)}/${encodeURIComponent(job.id)}/watch`)
  // A finished job still has output worth reading, but nothing to stream: ask for it once instead of
  // holding a connection open that will only send `done`.
  if (job.state === 'done' || job.state === 'failed' || job.state === 'killed') {
    void loadOnce(job.id)
    return
  }
  es = new EventSource(url)
  es.addEventListener('output', ev => {
    // EventSource splits on newlines, so one `event: output` per line arrives as one data line.
    output += ev.data + '\n'
    shownLines++
    paintOutput()
  })
  es.addEventListener('done', ev => {
    let code = null
    try {
      code = JSON.parse(ev.data).exitCode
    } catch {
      /* keep null */
    }
    output += `\n[finished${code === null ? '' : `, exit ${code}`}]\n`
    paintOutput()
    stopStream()
    // The list shows the job's state, so refresh it now that it has changed.
    void loadJobs()
  })
  es.addEventListener('error', ev => {
    if (ev.data) output += `\n[stream error: ${ev.data}]\n`
    paintOutput()
  })
  // A transport failure closes the stream; say so rather than leaving the pane looking idle.
  es.onerror = () => {
    if (es && es.readyState === EventSource.CLOSED) {
      output += '\n[stream closed]\n'
      paintOutput()
      stopStream()
    }
  }
}

async function loadOnce(id) {
  try {
    const d = await getJSON(`jobs/${encodeURIComponent(sbName)}/${encodeURIComponent(id)}/output`)
    output = (d.lines ?? []).join('\n')
    totalLines = d.totalLines ?? 0
    shownLines = (d.lines ?? []).length
    paintOutput()
  } catch (e) {
    output = e instanceof Error ? e.message : String(e)
    paintOutput()
  }
}

function stopStream() {
  if (es) {
    es.close()
    es = null
  }
}

/**
 * Open the machine's live screen.
 *
 * The stream needs a bearer, and a WebSocket handshake cannot carry an Authorization header from a
 * browser — so the token travels as a query parameter HERE, and only here. The extension checks it
 * before dialling the worker, so an unauthenticated caller never reaches a screen.
 *
 * This is a deliberate exception to the rule that tokens do not go in URLs: the alternative is no
 * screen at all, and the exposure is bounded by the same-origin proxy and a short-lived token.
 */
function startScreen(canvas) {
  stopScreen()
  screenState = 'connecting'
  // Built with a real query object rather than string concatenation: `withSession` may add the first
  // `?` or append to one, and appending "&token=" to a path with no query produced a broken URL.
  const q = new URLSearchParams()
  if (SESSION !== '') q.set('session', SESSION)
  q.set('token', token())
  const url = API + `sandboxes/${encodeURIComponent(sbName)}/screen?${q.toString()}`
  screen = createScreen(canvas, {
    url,
    onState(s) {
      screenState = s
      const el = document.getElementById('screen-state')
      if (el) el.textContent = s
    },
  })
}

function stopScreen() {
  if (screen) {
    screen.close()
    screen = null
  }
}

// ---- rendering ----
function el(tag, props = {}, kids = []) {
  const n = document.createElement(tag)
  for (const [k, v] of Object.entries(props)) {
    if (k === 'class') n.className = v
    else if (k === 'text') n.textContent = v
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v)
    else if (v !== null && v !== undefined) n.setAttribute(k, String(v))
  }
  for (const kid of kids) if (kid) n.append(kid)
  return n
}

function pill(text, cls) {
  return el('span', { class: `pill${cls ? ' ' + cls : ''}`, text })
}

function jobStatePill(state) {
  switch (state) {
    case 'running':
      return pill('running', 'run')
    case 'done':
      return pill('done', 'ok')
    case 'failed':
      return pill('failed', 'bad')
    case 'killed':
      return pill('killed', 'bad')
    default:
      return state ? pill(state) : null
  }
}

function render() {
  const app = document.getElementById('app')
  if (!app) return
  app.replaceChildren()

  const narrow = matchMedia('(max-width: 720px)').matches

  // ---- machines ----
  const railSb = el('aside', { class: `sandboxes${narrow && sbName !== '' ? ' hidden' : ''}` })
  if (sandboxes.length === 0) {
    railSb.append(el('div', { class: `state${listError ? ' err' : ''}`, text: listError || 'No sandboxes configured.' }))
  } else {
    for (const s of sandboxes) {
      railSb.append(
        el('button', {
          class: `row${s.name === sbName ? ' active' : ''}`,
          onclick: () => {
            sbName = s.name
            jobId = ''
            jobs = []
            output = ''
            stopStream()
            render()
            void loadJobs()
          },
        }, [
          el('span', { class: 'name', text: s.name }),
          el('span', { class: 'micro muted', text: [s.os, s.platform].filter(Boolean).join(' · ') || '—' }),
          el('span', { class: 'micro' }, [s.live ? pill('live', 'ok') : pill('down', 'bad')]),
        ]),
      )
    }
  }

  // ---- jobs ----
  const railJobs = el('aside', { class: `jobs${narrow && sbName === '' ? ' hidden' : ''}${narrow && jobId !== '' ? ' hidden' : ''}` })
  if (sbName === '') {
    railJobs.append(el('div', { class: 'state', text: 'Pick a machine.' }))
  } else if (jobs.length === 0) {
    railJobs.append(el('div', { class: `state${jobError ? ' err' : ''}`, text: jobError || 'No jobs yet.' }))
  } else {
    for (const j of jobs) {
      railJobs.append(
        el('button', {
          class: `row${j.id === jobId ? ' active' : ''}`,
          onclick: () => watch(j),
        }, [
          el('span', { class: 'name mono micro', text: j.id.slice(0, 10) }),
          el('span', { class: 'micro muted', text: (j.script ?? j.command ?? '').slice(0, 60) }),
          el('span', { class: 'micro' }, [jobStatePill(j.state)]),
        ]),
      )
    }
  }

  // ---- output ----
  const main = el('main', { class: narrow && jobId === '' ? 'hidden' : '' })
  if (sbName === '') {
    main.append(el('div', { class: 'state', text: 'Pick a machine.' }))
  } else {
    const sb = sandboxes.find(s => s.name === sbName)
    // Two views of the same machine: what it is RUNNING (screen) and what it PRODUCED (jobs). The
    // switch sits in the header because it changes what the whole pane means.
    const tabs = el('span', { class: 'tabs' }, [
      el('button', {
        class: `tab${view === 'screen' ? ' on' : ''}`,
        text: 'Screen',
        onclick: () => {
          view = 'screen'
          render()
        },
      }),
      el('button', {
        class: `tab${view === 'job' ? ' on' : ''}`,
        text: 'Output',
        onclick: () => {
          view = 'job'
          render()
        },
      }),
    ])
    const back = narrow
      ? el('button', {
          class: 'muted',
          text: '←',
          title: 'back',
          onclick: () => {
            jobId = ''
            stopStream()
            render()
          },
        })
      : null

    if (view === 'screen') {
      main.append(el('header', {}, [back, el('span', { class: 'title', text: sbName }), tabs]))
      // A headless machine has no screen to show, and offering one would be a permanent spinner.
      // The test is `screen` (a desktop is present), NOT `a11y` (an accessibility CLI is on PATH):
      // those are different questions, and a Wayland box can have a screen and no xa11y.
      if (sb && !sb.screen) {
        main.append(
          el('div', {
            class: 'state',
            text: `${sbName} is headless${sb.os ? ` (${sb.os})` : ''}. There is no screen to show.`,
          }),
        )
      } else {
        const canvas = el('canvas', { class: 'screen' })
        const status = el('div', { class: 'screen-state micro muted', id: 'screen-state', text: 'connecting' })
        main.append(el('div', { class: 'screen-wrap' }, [canvas, status]))
        // Started AFTER the canvas is in the document: the decoder paints into it, and a canvas that
        // is not mounted yet would be painted once and then thrown away by the next render.
        requestAnimationFrame(() => startScreen(canvas))
      }
    } else if (jobId === '') {
      main.append(el('header', {}, [back, el('span', { class: 'title', text: sbName }), tabs]))
      stopScreen()
      main.append(el('div', { class: 'state', text: 'Pick a job on the left.' }))
    } else {
      stopScreen()
      main.append(
        el('header', {}, [
          back,
          el('span', { class: 'title mono micro', text: jobId }),
          totalLines > 0 ? el('span', { class: 'micro muted', text: `${shownLines}/${totalLines} lines` }) : null,
          tabs,
        ]),
      )
      const term = el('pre', { class: 'term' })
      term.textContent = output === '' ? 'waiting for output…' : output
      term.id = 'term'
      main.append(term)
    }
  }

  app.append(railSb, railJobs, main)
  // Keep the newest line in view, which is what a reader watching a live stream expects.
  const t = document.getElementById('term')
  if (t) t.scrollTop = t.scrollHeight
}

/** Repaint only the output, so a live stream does not rebuild the rails on every line. */
function paintOutput() {
  const t = document.getElementById('term')
  if (!t) {
    render()
    return
  }
  t.textContent = output
  t.scrollTop = t.scrollHeight
}

// ---- theme ----
;(() => {
  const q = new URLSearchParams(location.search).get('theme')
  const m = q === 'dark' || q === 'light' ? q : matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  document.documentElement.dataset.theme = m
})()
window.addEventListener('message', ev => {
  const d = ev.data
  if (d && d.type === 'easyagent:theme' && (d.theme === 'dark' || d.theme === 'light')) {
    document.documentElement.dataset.theme = d.theme
  }
})

// ---- go ----
render()
// The hash decides what opens: a result about one job should land ON it, not on an empty picker.
void loadSandboxes().then(() => {
  if (sbName !== '') void loadJobs()
})
setInterval(() => void loadSandboxes(), 10000)
setInterval(() => {
  if (sbName !== '') void loadJobs()
}, 5000)
matchMedia('(max-width: 720px)').addEventListener('change', render)
// A stream outlives the page otherwise: the browser closes it, but only once the document is gone,
// and closing it here means the worker stops sending immediately.
window.addEventListener('pagehide', stopStream)

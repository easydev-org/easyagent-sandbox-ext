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
let sbName = ''
let jobId = ''
let listError = ''
let jobError = ''
let output = ''
/** The live EventSource, or null. Closed before a new one opens, so two do not interleave output. */
let es = null
let totalLines = 0
let shownLines = 0

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
  if (jobId === '') {
    main.append(el('div', { class: 'state', text: sbName === '' ? 'Pick a machine.' : 'Pick a job.' }))
  } else {
    const back = narrow
      ? el('button', { class: 'muted', text: '←', title: 'back', onclick: () => { jobId = ''; stopStream(); render() } })
      : null
    main.append(
      el('header', {}, [
        back,
        el('span', { class: 'title mono micro', text: jobId }),
        totalLines > 0 ? el('span', { class: 'micro muted', text: `${shownLines}/${totalLines} lines` }) : null,
      ]),
    )
    const term = el('pre', { class: 'term' })
    term.textContent = output === '' ? 'waiting for output…' : output
    term.id = 'term'
    main.append(term)
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
void loadSandboxes()
setInterval(() => void loadSandboxes(), 10000)
setInterval(() => {
  if (sbName !== '') void loadJobs()
}, 5000)
matchMedia('(max-width: 720px)').addEventListener('change', render)
// A stream outlives the page otherwise: the browser closes it, but only once the document is gone,
// and closing it here means the worker stops sending immediately.
window.addEventListener('pagehide', stopStream)

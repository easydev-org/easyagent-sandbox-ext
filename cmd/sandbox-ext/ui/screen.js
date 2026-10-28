// Live screen: the machine's H.264 stream, decoded in the browser.
//
// WHY THIS SHAPE. The worker sends Annex-B H.264 access units over one WebSocket. A browser cannot
// hand that to <video>, and putting it in an MP4 container would need a muxer and add latency — but
// it CAN decode it directly with WebCodecs' `VideoDecoder` and paint each frame to a canvas. No
// WebRTC, no UDP, no STUN: the same 443/TCP connection the page already uses.
//
// THE PROTOCOL, from the worker's side:
//   1. TEXT   {"t":"hello","codec":"avc1.42E01F","w":1280,"h":720,"fps":10}
//   2. BINARY one access unit that carries SPS + PPS + an IDR frame
//   3. BINARY one access unit per frame thereafter
//
// The first binary frame is a KEYFRAME BY CONSTRUCTION — the worker waits for one — which is why the
// decoder can be configured from the hello and started on the very next message.

/**
 * The NAL unit types inside an Annex-B buffer.
 *
 * `VideoDecoder` wants one `EncodedVideoChunk` per ACCESS UNIT with its NALs concatenated, so this
 * only needs to find where units begin — enough to tell a keyframe from a delta frame.
 */
function nalTypes(buf) {
  const out = []
  const u8 = new Uint8Array(buf)
  for (let i = 0; i + 3 < u8.length; i++) {
    // A 3-byte (00 00 01) or 4-byte (00 00 00 01) start code.
    if (u8[i] === 0 && u8[i + 1] === 0 && (u8[i + 2] === 1 || (u8[i + 2] === 0 && u8[i + 3] === 1))) {
      const at = u8[i + 2] === 1 ? i + 3 : i + 4
      if (at < u8.length) out.push(u8[at] & 0x1f)
    }
  }
  return out
}

/**
 * A screen player for one sandbox.
 *
 * `onState` receives a short label so the panel can distinguish a stream that is waiting for a
 * keyframe, decoding, or has stopped from one that is simply idle.
 */
export function createScreen(canvas, { url, onState }) {
  let ws = null
  let decoder = null
  let closed = false
  /** Frames before the decoder is configured are dropped: the worker's first frame is a keyframe, and
   *  anything earlier could not be decoded in order anyway. */
  let ready = false
  let frames = 0
  let lastReport = 0

  const state = s => onState?.(s)

  function configure(hello) {
    if (typeof VideoDecoder === 'undefined') {
      state('no webcodecs')
      return false
    }
    decoder = new VideoDecoder({
      output(frame) {
        // Sized from the FRAME, not from the hello: an encoder can report a nominal size and emit
        // another, and a canvas that disagrees stretches the picture.
        if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
          canvas.width = frame.displayWidth
          canvas.height = frame.displayHeight
        }
        const ctx = canvas.getContext('2d')
        if (ctx) ctx.drawImage(frame, 0, 0)
        frame.close()
        frames++
        const now = performance.now()
        if (now - lastReport > 1000) {
          lastReport = now
          state(`live ${hello.w}x${hello.h}`)
        }
      },
      error(e) {
        // One bad frame is usually recoverable: the next keyframe re-syncs. Report it rather than
        // tearing the connection down, which would lose the keyframe that fixes it.
        state('decode error: ' + (e?.message ?? String(e)))
      },
    })
    decoder.configure({ codec: hello.codec, optimizeForLatency: true })
    ready = true
    return true
  }

  function feed(buf, timestampUs, keyFrame) {
    if (!ready || decoder === null) return
    // Backpressure: the decoder queues work. Feeding it without bound turns a slow CPU into unbounded
    // memory and a stream that drifts further behind the machine every second.
    if (decoder.decodeQueueSize > 12) return
    try {
      decoder.decode(new EncodedVideoChunk({
        type: keyFrame ? 'key' : 'delta',
        timestamp: timestampUs,
        data: buf,
      }))
    } catch (e) {
      state('feed error: ' + (e?.message ?? String(e)))
    }
  }

  function open() {
    state('connecting')
    ws = new WebSocket(url)
    ws.binaryType = 'arraybuffer'
    let hello = null
    let pts = 0
    // A fixed step keeps timestamps monotonic even when a frame is skipped for backpressure.
    const stepUs = () => Math.round(1_000_000 / (hello?.fps || 10))

    ws.onmessage = ev => {
      if (closed) return
      if (typeof ev.data === 'string') {
        try {
          const msg = JSON.parse(ev.data)
          if (msg.t === 'hello') {
            hello = msg
            state('starting')
            configure(msg)
          }
        } catch {
          /* an unreadable hello leaves the decoder unconfigured, and the state says so */
        }
        return
      }
      const buf = ev.data
      const types = nalTypes(buf)
      // NAL type 5 is an IDR slice, 7 is SPS. The worker's first frame carries both.
      const keyFrame = types.includes(5) || types.includes(7)
      feed(buf, pts, keyFrame)
      pts += stepUs()
    }

    ws.onclose = () => {
      if (!closed) state('disconnected')
    }
    ws.onerror = () => {
      if (!closed) state('connection error')
    }
  }

  open()

  return {
    get frameCount() {
      return frames
    },
    close() {
      closed = true
      ready = false
      if (ws) {
        ws.close()
        ws = null
      }
      if (decoder && decoder.state !== 'closed') {
        try {
          decoder.close()
        } catch {
          /* already closing */
        }
      }
      decoder = null
    },
  }
}

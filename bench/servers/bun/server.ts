// dyno bench server: Bun.serve, no dependencies.
import indexHtml from "../../static/index.html" with { type: "text" };

const PAYLOAD_SIZE = 1 << 20; // 1 MiB of random bytes, generated once
const WRITE_CHUNK = 256 << 10; // bytes enqueued per pull
const MAX_DOWNLOAD = 2 ** 34;

const payload = new Uint8Array(PAYLOAD_SIZE);
for (let i = 0; i < PAYLOAD_SIZE; i += 65536) crypto.getRandomValues(payload.subarray(i, i + 65536));
// Pre-sliced views so the hot loop allocates no payload memory.
const chunks: Uint8Array[] = [];
for (let off = 0; off < PAYLOAD_SIZE; off += WRITE_CHUNK) chunks.push(payload.subarray(off, off + WRITE_CHUNK));

function download(n: number): Response {
  let left = n;
  let i = 0;
  // A pull-based stream: Bun calls pull() only when the socket has drained, so a slow client
  // holds one chunk in memory, not the whole response. (A `type: "direct"` stream with
  // `await controller.flush()` buffered the whole 32 MiB response per slow client in Bun 1.4.2.)
  const body = new ReadableStream(
    {
      pull(controller) {
        if (left <= 0) {
          controller.close();
          return;
        }
        const c = chunks[i]!;
        const piece = left >= c.byteLength ? c : c.subarray(0, left);
        left -= piece.byteLength;
        i = (i + 1) % chunks.length;
        controller.enqueue(piece);
      },
    },
    { highWaterMark: 0 },
  );
  return new Response(body, {
    headers: {
      "content-type": "application/octet-stream",
      "cache-control": "no-store",
      "content-length": String(n), // Bun 1.4.2 ignores this for stream bodies and sends chunked
    },
  });
}

const htmlHeaders = { "content-type": "text/html; charset=utf-8" };

Bun.serve({
  hostname: "127.0.0.1",
  port: Number(process.env.PORT ?? 8080),
  idleTimeout: 60,
  routes: {
    "/": new Response(indexHtml, { headers: htmlHeaders }),
    "/ping": new Response(null, { status: 204, headers: { "cache-control": "no-store" } }),
    "/dl": {
      GET(req) {
        const q = new URL(req.url).searchParams.get("bytes");
        const n = Number(q);
        if (q === null || !Number.isSafeInteger(n) || n < 0 || n > MAX_DOWNLOAD) return new Response("bad bytes\n", { status: 400 });
        return download(n);
      },
    },
    "/ul": {
      async POST(req) {
        let total = 0;
        if (req.body) for await (const chunk of req.body) total += chunk.byteLength;
        return new Response(String(total), { headers: { "content-type": "text/plain" } });
      },
    },
  },
  fetch() {
    return new Response("not found\n", { status: 404 });
  },
});

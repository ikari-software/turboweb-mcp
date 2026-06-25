#!/usr/bin/env node
// Cross-origin frame test harness server.
//
// Serves the same static pages on THREE ports. Origin = scheme+host+PORT, so a
// page on :8080 embedding an iframe from :8081 is genuinely cross-origin — which
// is exactly what we need to exercise the all_frames frameId handshake. No deps.
//
//   node testpages/serve.js            # ports 8080,8081,8082
//   PORTS=9000,9001,9002 node serve.js # override
//
// Then open http://127.0.0.1:8080/ in the browser the extension is attached to.

const http = require('http');
const fs = require('fs');
const path = require('path');

const ROOT = __dirname;
const PORTS = (process.env.PORTS || '8080,8081,8082').split(',').map((p) => parseInt(p.trim(), 10));
const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8' };

function handler(req, res) {
  let pathname = '/';
  try { pathname = decodeURIComponent(new URL(req.url, 'http://x').pathname); } catch { /* keep / */ }
  if (pathname === '/') pathname = '/top.html';

  const file = path.normalize(path.join(ROOT, pathname));
  if (!file.startsWith(ROOT)) { res.writeHead(403); return res.end('forbidden'); }

  fs.readFile(file, (err, buf) => {
    if (err) { res.writeHead(404, { 'content-type': 'text/plain' }); return res.end('not found: ' + pathname); }
    res.writeHead(200, {
      'content-type': MIME[path.extname(file)] || 'application/octet-stream',
      // No X-Frame-Options / frame-ancestors so the pages can embed each other.
      'cache-control': 'no-store',
    });
    res.end(buf);
  });
}

for (const port of PORTS) {
  http.createServer(handler).listen(port, '127.0.0.1', () => {
    console.log(`serving testpages on http://127.0.0.1:${port}/`);
  });
}

console.log('\nOpen the TOP page (origin A) here:  http://127.0.0.1:' + PORTS[0] + '/');
console.log('Ports → origins:  A=' + PORTS[0] + '  B=' + PORTS[1] + '  C=' + PORTS[2] + '\n');

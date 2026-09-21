// --fixtures enables isolated UI examples, never the production API or database.
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '../../internal/knowledge/web');
const assets = {'/knowledge':'index.html', '/knowledge/app.js':'app.js', '/knowledge/conversations.js':'conversations.js', '/knowledge/style.css':'style.css'};
const fixture = process.argv.includes('--fixtures') ? require('./ui-fixture.cjs') : null;
const server = http.createServer((req, res) => {
  if (req.url === '/favicon.ico') { res.writeHead(204); res.end(); return; }
  if (fixture && req.url.startsWith('/api/')) { fixture(req, res); return; }
  const asset = assets[new URL(req.url, 'http://localhost').pathname];
  if (!asset) {res.writeHead(404);res.end();return;}
  res.setHeader('Content-Type', asset.endsWith('.js') ? 'text/javascript' : asset.endsWith('.css') ? 'text/css' : 'text/html; charset=utf-8');
  fs.createReadStream(path.join(root, asset)).pipe(res);
});
server.listen(0, '127.0.0.1', () => console.log(`http://127.0.0.1:${server.address().port}/knowledge${fixture ? ' (UI fixture only)' : ''}`));

// Drives the real web UI: serves the page with test.js appended (proxying /api to a
// running node on :18080) into headless Chrome and prints test.js's PASS/FAIL lines.
// usage: node run.js <index.html> <test.js> <chrome-profile-dir>
const http = require('http'), fs = require('fs'), cp = require('child_process');
const page = fs.readFileSync(process.argv[2], 'utf8');
const test = fs.readFileSync(process.argv[3], 'utf8');
const html = page.replace('</body>', '<pre id="result"></pre><script>' + test + '</script></body>');
// headless --dump-dom has a 0x0 viewport, so the page runs in a desktop-sized iframe and posts its result up
const frame = '<body style="margin:0"><iframe src="/app" style="width:1200px;height:800px;border:0"></iframe><pre id="result"></pre>' +
  '<script>addEventListener("message",e=>{document.getElementById("result").textContent=e.data})</script></body>';
const srv = http.createServer((req, res) => {
  if (req.url === '/') { res.setHeader('content-type', 'text/html'); return res.end(frame); }
  if (req.url === '/app') { res.setHeader('content-type', 'text/html'); return res.end(html); }
  const p = http.request({ host: '127.0.0.1', port: 18080, path: req.url, method: req.method, headers: req.headers },
    r => { res.writeHead(r.statusCode, r.headers); r.pipe(res); });
  req.pipe(p);
  p.on('error', () => { res.statusCode = 502; res.end(); });
}).listen(18081, () => {
  // async, not spawnSync: the proxy above must keep answering while Chrome runs
  cp.execFile('google-chrome', ['--headless=new', '--disable-gpu', '--no-sandbox', '--user-data-dir=' + process.argv[4],
    '--virtual-time-budget=90000', '--dump-dom', 'http://127.0.0.1:18081/'], { encoding: 'utf8', timeout: 240000, maxBuffer: 1 << 26 },
  (err, stdout, stderr) => {
    const m = /<pre id="result">([\s\S]*?)<\/pre>/.exec(stdout || '');
    const text = m ? m[1].replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&') : 'NO RESULT\n' + (stderr || '').slice(-500);
    console.log(text);
    srv.close();
    process.exit(/^(FAIL|EXCEPTION|NO RESULT)/m.test(text) || !/DONE/.test(text) ? 1 : 0);
  });
});

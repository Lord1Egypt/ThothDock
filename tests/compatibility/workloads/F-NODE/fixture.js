// Runtime fixture for F-NODE: one PASS/FAIL line per check.
const { Worker } = require('worker_threads');
const cp = require('child_process'), dns = require('dns'), fs = require('fs'), https = require('https'), os = require('os'), path = require('path');
const check = (ok, what) => console.log((ok ? 'PASS ' : 'FAIL ') + what);
(async () => {
  const sums = await Promise.all([1, 2, 3, 4].map(() => new Promise((res, rej) => {
    const w = new Worker('const {parentPort}=require("worker_threads"); let s=0; for (let i=0;i<1e6;i++) s+=i; parentPort.postMessage(s)', { eval: true });
    w.on('message', res); w.on('error', rej);
  })));
  check(sums.every(s => s === 499999500000), 'node: 4 worker threads');
  const r = cp.spawnSync('sh', ['-c', 'echo sub; exit 3']);
  check(r.stdout.toString() === 'sub\n' && r.status === 3, 'node: child process output and exit code');
  const piped = cp.execSync('tr a-z A-Z', { input: 'piped' }).toString();
  check(piped === 'PIPED', 'node: pipes to a child');
  const d = fs.mkdtempSync(path.join(os.tmpdir(), 'n'));
  fs.writeFileSync(path.join(d, 'f'), Buffer.alloc(1 << 20, 1)); fs.renameSync(path.join(d, 'f'), path.join(d, 'g'));
  check(fs.statSync(path.join(d, 'g')).size === 1 << 20, 'node: write and rename 1 MiB');
  const got = await new Promise(res => { process.once('SIGUSR1', () => res(true)); process.kill(process.pid, 'SIGUSR1'); });
  check(got, 'node: signal handler');
  try { await dns.promises.lookup('dns.google'); check(true, 'node: DNS lookup'); } catch (e) { check(false, 'node: DNS lookup (' + e.code + ')'); }
  await new Promise(res => https.get('https://dns.google/resolve?name=example.com', { timeout: 15000 }, r => {
    let b = ''; r.on('data', c => b += c); r.on('end', () => { check(b.includes('Answer'), 'node: HTTPS request (certificate verified)'); res(); });
  }).on('error', e => { check(false, 'node: HTTPS request (' + e.code + ')'); res(); }));
})();

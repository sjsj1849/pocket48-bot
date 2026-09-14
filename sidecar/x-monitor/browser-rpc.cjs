const fs = require('fs'), path = require('path'), crypto = require('crypto');
const WebSocket = require('../weibo-auth/node_modules/ws');
const root = path.resolve(__dirname, '../..');
const cmd = process.argv[2];
if (!['x_panel_password', 'x_panel_diagnose', 'x_panel_resume', 'x_panel_login', 'x_panel_verify', 'x_panel_sync'].includes(cmd)) process.exit(2);
const endpoint = JSON.parse(fs.readFileSync(path.join(root, 'storage/browser-sidecar.json'), 'utf8'));
if (!Number.isInteger(endpoint.port) || endpoint.port < 1 || endpoint.port > 65535) process.exit(2);
const id = crypto.randomUUID();
const socket = new WebSocket(`ws://127.0.0.1:${endpoint.port}/`, { maxPayload: 1024 * 1024 });
const timer = setTimeout(() => { socket.terminate(); process.exitCode = 1; }, 80000);
socket.on('open', () => socket.send(JSON.stringify({ cmd, requestId: id })));
socket.on('message', raw => {
  let result; try { result = JSON.parse(String(raw)); } catch { return; }
  if (result.requestId !== id || !['x_login_result', 'x_panel_result'].includes(result.type)) return;
  // stdout is a private pipe captured by login-worker.py, not a service log.
  process.stdout.write(JSON.stringify(result)); clearTimeout(timer); socket.close();
});
socket.on('error', () => { clearTimeout(timer); process.exitCode = 1; });

import fs from 'node:fs';
import process from 'node:process';
import { AndroidQChatClient } from '../android-qchat.mjs';

const [configPath, serverIdArg, listenSecondsArg = '20'] = process.argv.slice(2);
if (!configPath || !serverIdArg) {
  process.stderr.write('usage: node probe-qchat.mjs <private-config.json> <serverId> [listenSeconds]\n');
  process.exit(2);
}

const config = JSON.parse(fs.readFileSync(configPath, 'utf8'));
const received = [];
const client = new AndroidQChatClient({
  appKey: '632feff1f4c838541ab75195d1ceb3fa',
  account: String(config.NIM_ACCOUNT || ''),
  token: String(config.NIM_TOKEN || ''),
  onConnected() { process.stderr.write('QChat connected\n'); },
  onDisconnected() { process.stderr.write('QChat disconnected\n'); },
  onError(error) { process.stderr.write(`QChat error: ${error?.message || error}\n`); },
  onMessage(message) {
    received.push({
      serverId: message.serverId,
      channelId: message.channelId,
      type: message.type,
      time: message.time
    });
  }
});

try {
  await client.connect([Number(serverIdArg)]);
  await new Promise((resolve) => setTimeout(resolve, Number(listenSecondsArg) * 1000));
  process.stdout.write(`${JSON.stringify({ connected: true, received })}\n`);
  client.disconnect();
} catch (error) {
  process.stdout.write(`${JSON.stringify({ connected: false, error: error?.message || String(error) })}\n`);
  client.disconnect();
  process.exitCode = 1;
}

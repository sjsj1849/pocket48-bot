import fs from 'node:fs';
import crypto from 'node:crypto';

const config = JSON.parse(fs.readFileSync('/root/pocket48-bot/config.json', 'utf8'));
const roomID = Number(process.argv[2] || '1279287');

const timestamp = Date.now();
const random = Math.floor(Math.random() * 10000);
const paSecret = '40F1065D8E71F2A2A2BBE3F6F3D8B8C9';
const hash = crypto.createHash('md5').update(`${timestamp}${random}${paSecret}`).digest('hex');
const pa = Buffer.from(`${timestamp},${random},${hash},`).toString('base64');

const url = 'https://pocketapi.48.cn/im/api/v1/team/detail';
const resp = await fetch(url, {
  method: 'POST',
  headers: {
    'Content-Type': 'application/json;charset=utf-8',
    'token': config.POCKET_TOKEN,
    'pa': pa,
    'User-Agent': 'PocketFans201807/7.1.37 (iPhone; iOS 26.2.1; Scale/3.00)',
  },
  body: JSON.stringify({ serverId: 1, channelId: roomID })
});

const data = await resp.json();
const content = data.content || data.Content || {};
console.log('Room info for', roomID);
console.log('ServerID:', content.serverId || content.ServerId || content.server_id);
console.log('ChannelID:', content.channelId || content.ChannelId || content.channel_id);
console.log('OwnerName:', content.ownerName || content.OwnerName || content.owner_name);
console.log('Full:', JSON.stringify(content, null, 2).slice(0, 2000));

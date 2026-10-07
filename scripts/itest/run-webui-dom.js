// yaa WebUI 场景测试 v4 — 纯 DOM 行为断言（生产构建）
const wsUrl = process.argv[2];
const ws = new WebSocket(wsUrl);
let id = 0; const pend = new Map();
const errs = [];
const say = (...a) => console.log('[D]', ...a);
let fails = 0;
function ok(n, c, e) { if (c) say('  ✓ ' + n); else { fails++; say('  ✗ ' + n + (e !== undefined ? ' → ' + e : '')); } }
ws.onmessage = (evr) => {
  const m = JSON.parse(String(evr.data));
  if (m.id) { const p = pend.get(m.id); if (p) { pend.delete(m.id); p(m); } }
  else if (m.method === 'Runtime.exceptionThrown') {
    const d = m.params.exceptionDetails;
    errs.push('[EXC] ' + (d.exception && (d.exception.description || d.exception.value) || d.text || '').slice(0, 220));
  } else if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error') {
    errs.push('[CE] ' + (m.params.args || []).map(a => a.value || a.description || '').join(' ').slice(0, 220));
  }
};
const send = (method, params = {}) => new Promise(r => { const i = ++id; pend.set(i, r); ws.send(JSON.stringify({ id: i, method, params })); });
const sleep = ms => new Promise(r => setTimeout(r, ms));
async function ev(expression) {
  const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  const res = r.result;
  if (res.exceptionDetails) return 'THREW';
  return res.result ? res.result.value : 'NO-VALUE';
}
async function waitTrue(expression, timeout = 15000, iv = 300) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeout) {
    const v = await ev(expression);
    if (v === true || v === 'true' || (typeof v === 'string' && v.length > 0 && v !== 'THREW') || Number(v) > 0) return true;
    await sleep(iv);
  }
  return false;
}
const typeText = (sel, text) => `(() => {
  var el = document.querySelector('${sel}');
  if (!el) return 'no-el';
  var proto = el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
  var setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
  setter.call(el, ${JSON.stringify(text)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return 'typed';
})()`;

(async () => {
  await new Promise((res, rej) => { ws.addEventListener('open', res); ws.addEventListener('error', rej); });
  await send('Runtime.enable'); await send('Network.enable'); await send('Network.clearBrowserCache');
  await send('Page.navigate', { url: 'http://127.0.0.1:18081/?dom=1' });
  await sleep(4000);
  // 清掉上次运行残留（token / dark 主题），保证 S1 是无 token + light
  await ev(`(function(){ try { localStorage.setItem('yaa_ui', JSON.stringify({ baseURL: '', token: '', theme: 'light' })); } catch(e){} return 'cleared'; })()`);
  await send('Page.navigate', { url: 'http://127.0.0.1:18081/?dom=1f' });
  await sleep(3000);

  say('== S1 无 token 启动（优雅降级 + 401 提示） ==');
  ok('侧栏已渲染（booted）', await ev(`!!document.querySelector('.sidebar')`));
  ok('401 提示 toast', await waitTrue(`document.querySelectorAll('.el-message--error').length > 0`, 20000, 200));
  ok('欢迎页显示（无 agent）', await waitTrue(`document.querySelector('.welcome') !== null`));
  say('  toasts:', await ev(`Array.prototype.map.call(document.querySelectorAll('.el-message'), function(e){return e.textContent;}).join(' | ')`));

  say('== S2 设置 token → 保存并重连 ==');
  await ev(`document.querySelectorAll('.foot-btn')[0].click(); 'ok'`);
  await sleep(1000);
  ok('设置对话框打开', await ev(`!!document.querySelector('.settings-dialog')`));
  await ev(typeText('.settings-dialog input[type="password"]', 'itest-secret-token'));
  await sleep(300);
  await ev(`(() => { var b = Array.prototype.find.call(document.querySelectorAll('.settings-dialog .el-button'), function (x) { return x.textContent.indexOf('保存并重连') >= 0; }); b && b.click(); return 'ok'; })()`);
  ok('重连后 agents 加载（出现 Agent 选择）', await waitTrue(`document.querySelectorAll('.session-item').length >= 1 || document.querySelector('.el-select__selected-item') !== null`, 25000, 500));
  const agentLabel = await ev(`(document.querySelector('.el-select__selected-item')||document.querySelector('.agent-opt')||{textContent:''}).textContent`);
  say('  当前 agent:', String(agentLabel).slice(0, 50));

  say('== S3 新建会话 ==');
  const ncDisabled = Number(await ev(`(function(){var b=document.querySelector('.new-chat');return b?b.disabled:'no-btn';})()`));
  say('  S3 new-chat disabled:', String(ncDisabled));
  await ev(`document.querySelector('.new-chat').click(); 'ok'`);
  const samples = [];
  for (var i = 0; i < 30; i++) {
    await sleep(500);
    samples.push(await ev(`JSON.stringify({active:document.querySelectorAll('.session-item.active').length, total:document.querySelectorAll('.session-item').length, sel:(document.querySelector('.el-select__selected-item')||{textContent:'-'}).textContent.slice(0,20), title:(document.querySelector('.chat-title')||{textContent:'-'}).textContent.slice(0,24), toast:Array.prototype.map.call(document.querySelectorAll('.el-message'),function(e){return e.textContent;}).join('|')})`));
    if (samples[samples.length-1].includes('"active":1')) { say('  S3 reached active=1 at sample ' + (i+1)); break; }
  }
  say('  S3 samples(head):', samples.slice(0,5).join(' ;; '));
  say('  S3 samples(tail):', samples.slice(-3).join(' ;; '));
  ok('会话已创建并被选中', await ev(`document.querySelectorAll('.session-item.active').length >= 1`));

  say('== S4 普通消息（流式渲染） ==');
  const typed = await ev(typeText('.composer textarea', '你好，介绍一下自己'));
  await sleep(300);
  const taVal = await ev(`(document.querySelector('.composer textarea')||{value:'no-ta'}).value`);
  const btnDis = await ev(`document.querySelector('.send-btn') ? document.querySelector('.send-btn').disabled : 'no-btn'`);
  const stateInd = await ev(`!!document.querySelector('.chat-state')`);
  const taDis = await ev(`(document.querySelector('.composer textarea')||{disabled:null}).disabled`);
  const sCount = await ev(`document.querySelectorAll('.session-item').length`);
  const activeCls = await ev(`document.querySelectorAll('.session-item.active').length`);
  say('  S4 diag: typed=' + String(typed).slice(0,8) + ' ta=' + String(taVal).slice(0,12)
      + ' disabled=' + String(btnDis) + ' chatState=' + String(stateInd)
      + ' taDisabled=' + String(taDis) + ' sessions=' + String(sCount) + ' active=' + String(activeCls));
  await ev(`document.querySelector('.send-btn').click(); 'ok'`);
  ok('用户消息渲染', await waitTrue(`document.querySelectorAll('.msg.user').length > 0`, 8000));
  ok('assistant markdown 渲染', await waitTrue(`document.querySelectorAll('.msg.assistant .md').length > 0 && document.querySelector('.msg.assistant .md').textContent.length > 5`, 30000, 400));
  const md1 = await ev(`document.querySelector('.msg.assistant .md').textContent.slice(0,50)`);
  say('  md:', String(md1));

  say('== S5 工具调用（shell） ==');
  await ev(typeText('.composer textarea', '!!tool shell {"command":"echo webui-tool-ok"}'));
  await sleep(300);
  await ev(`document.querySelector('.send-btn').click(); 'ok'`);
  ok('工具卡片出现', await waitTrue(`document.querySelectorAll('.tool-call').length > 0`, 30000, 400));
  const tn = await ev(`(document.querySelector('.tool-name')||{textContent:''}).textContent`);
  ok('工具名 = shell', String(tn) === 'shell', String(tn));
  if (String(tn) !== 'shell') {
    const tn2 = await waitTrue(`(function(){ var n=(document.querySelector('.tool-name')||{textContent:''}).textContent; return n === 'shell'; })()`, 8000, 300);
    ok('工具名 = shell（延迟重试）', tn2);
    if (!tn2) {
      const dump = await ev(`JSON.stringify({ toolCards: document.querySelectorAll('.tool-call').length,
        heads: Array.prototype.map.call(document.querySelectorAll('.tool-call'), function(e){ return e.textContent.replace(/\s+/g,' ').slice(0,80); }),
        groups: Array.prototype.map.call(document.querySelectorAll('.msg'), function(e){ return (e.className||'') + ':' + e.textContent.slice(0,40); }).slice(0,8) })`);
      say('  S5 dump:', String(dump));
    }
  }
  ok('工具结果回显', await waitTrue(`(function(){ var all = ''; var els = document.querySelectorAll('.tool-call'); for (var i=0;i<els.length;i++){ all += els[i].textContent; } return all.indexOf('webui-tool-ok') >= 0; })()`, 15000, 400));

  say('== S6 中止慢速生成 ==');
  await ev(typeText('.composer textarea', '!!slow 慢慢说'));
  await sleep(300);
  await ev(`document.querySelector('.send-btn').click(); 'ok'`);
  ok('慢速流式指示出现', await waitTrue(`document.querySelector('.chat-state') !== null`, 10000, 200));
  ok('停止按钮出现', await waitTrue(`document.querySelector('.send-btn.stop') !== null`, 10000, 200));
  await ev(`(function(){ var b = document.querySelector('.send-btn.stop'); if (b) { b.click(); return 'clicked'; } return 'none'; })()`);
  ok('停止指示消失', await waitTrue(`document.querySelector('.chat-state') === null`, 15000, 400));
  ok('停止后提示出现', await waitTrue(`document.querySelector('.msg.error') !== null`, 15000, 400), await ev(`Array.prototype.map.call(document.querySelectorAll('.msg.error'),function(e){return e.textContent;}).join('|')`));

  say('== S7 深色主题 ==');
  ok('初始 light', await ev(`!document.documentElement.classList.contains('dark')`));
  await ev(`(() => { var b = Array.prototype.find.call(document.querySelectorAll('.foot-btn'), function (x) { return x.textContent.indexOf('深色') >= 0 || x.textContent.indexOf('浅色') >= 0; }); b && b.click(); return 'ok'; })()`);
  await sleep(600);
  ok('dark 生效', await ev(`document.documentElement.classList.contains('dark')`));

  say('== S8 删除会话 ==');
  const before = Number(await ev(`document.querySelectorAll('.session-item').length`));
  await ev(`(function(){ var items = document.querySelectorAll('.session-item'); if (items[0]) { items[0].querySelector('.session-more').click(); return 'open'; } return 'none'; })()`);
  await sleep(900);
  await ev(`(function(){ var d = Array.prototype.find.call(document.querySelectorAll('.el-dropdown-menu__item'), function (x) { return x.textContent.indexOf('删除') >= 0; }); d && d.click(); return 'del'; })()`);
  await sleep(900);
  ok('确认框出现', await waitTrue(`document.querySelector('.el-message-box .el-button--primary') !== null`, 6000));
  await ev(`document.querySelector('.el-message-box .el-button--primary').click(); 'ok'`);
  await sleep(2500);
  const after = Number(await ev(`document.querySelectorAll('.session-item').length`));
  ok('会话已删除', after < before, 'before=' + before + ' after=' + after);

  await sleep(500);
  say('== 控制台/异常汇总 ==');
  const real = errs.filter(e => e.indexOf('[CE]') >= 0 || e.indexOf('[EXC]') >= 0);
  ok('无 console 错误/异常', real.length === 0, real.slice(0, 4).join(' ;; '));
  real.slice(0, 4).forEach(e => say('  ' + e));
  say('fails=' + fails);
  process.exit(fails ? 1 : 0);
})().catch(e => { console.error('[D] ERR', e.stack || e.message); process.exit(1); });
setTimeout(() => { console.error('[D] HARD TIMEOUT'); process.exit(2); }, 160000);
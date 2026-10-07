/* Yaa! WebUI —— ChatGPT / Chatbox 风格对话控制台
 * 依赖（本地 vendor，离线可用）：Vue3 + Element Plus + Icons + marked + DOMPurify
 * 传输：SSE 订阅增量 + REST POST 触发 turn（token 经 Authorization 头）
 */
const { createApp, ref, reactive, computed, watch, nextTick, onMounted } = Vue;

marked.setOptions({ gfm: true, breaks: false });

let bootPromise = null;

const app = createApp({
  setup() {
    // ---------- 状态 ----------
    const booted = ref(false);
    const connected = ref(false);
    const streaming = ref(false);
    const sseOk = ref(false);
    const agents = ref([]);
    const providers = ref([]);
    const agentId = ref('');
    const sessions = ref([]);
    const sessionId = ref('');
    const messages = ref([]);
    const draft = ref('');
    const settingsOpen = ref(false);
    const settings = reactive({ baseURL: '', token: '', theme: 'light' });
    const lastUsage = ref(null);
    const msgBox = ref(null);
    const inputBox = ref(null);
    const mdBox = ref(null);

    // 当前 turn 的实时状态
    const live = reactive({ turnId: '', text: '', reasoning: '', tools: [], done: false, error: '' });

    let sseAbort = null;      // 当前 SSE AbortController
    let sseReconnectTimer = null;
    let currentTurnAbort = null;
    let mdRaf = 0;

    // ---------- 派生 ----------
    const currentAgent = computed(() => agents.value.find(a => a.id === agentId.value) || null);
    const currentSession = computed(() => sessions.value.find(s => s.id === sessionId.value) || null);
    const canSend = computed(() => !!sessionId.value && !!draft.value.trim() && !streaming.value);

    const hints = [
      '帮我总结一下这个项目',
      '列出当前可用的工具',
      '你好，介绍一下你自己',
      '用幽默的方式解释什么是 Agent',
    ];

    // ---------- 工具函数 ----------
    function headers(extra) {
      const h = Object.assign({ 'Content-Type': 'application/json' }, extra || {});
      if (settings.token) h['Authorization'] = 'Bearer ' + settings.token;
      return h;
    }

    function apiBase() {
      return (settings.baseURL || '').replace(/\/+$/, '');
    }

    async function api(path, opts = {}) {
      const res = await fetch(apiBase() + path, Object.assign({ headers: headers() }, opts));
      let body = null;
      try { body = await res.json(); } catch (e) { /* ignore */ }
      if (!res.ok || !body || body.code !== 0) {
        const msg = (body && body.message) ? `${body.message} (${body.code || res.status})` : ('HTTP ' + res.status);
        const err = new Error(msg);
        err.status = res.status;
        throw err;
      }
      return body.data;
    }

    function notify(msg, type = 'info') {
      ElMessage({ message: msg, type, grouping: true, duration: 2200 });
    }

    function newTurnId() {
      return 'turn_' + (crypto.randomUUID ? crypto.randomUUID().replace(/-/g, '') : String(Date.now()) + Math.random().toString(36).slice(2, 10));
    }

    function uuidShort() {
      return (crypto.randomUUID ? crypto.randomUUID() : String(Date.now()) + Math.random().toString(36).slice(2, 10));
    }

    function prettyJSON(s) {
      if (!s) return '';
      try { return JSON.stringify(JSON.parse(s), null, 1); } catch (e) { return s; }
    }

    function truncate(s, n) {
      if (!s) return '';
      return s.length > n ? s.slice(0, n) + '…' : s;
    }

    function statusLabel(s) {
      return { running: '运行中', paused: '已暂停', stopped: '已停止' }[s] || s;
    }

    function sessionTitle(s) {
      if (!s) return '';
      const t = (s.metadata && s.metadata.title) || '';
      if (t) return t;
      const first = firstUserText(s);
      return first ? first.slice(0, 28) : (s.id ? s.id.slice(0, 16) : '新会话');
    }

    function firstUserText(s) {
      const m = messages.value.find(x => x.session_id === s.id && x.role === 'user');
      return m ? m.content : '';
    }

    // ---------- 设置 / 主题 ----------
    function loadSettings() {
      try {
        const st = JSON.parse(localStorage.getItem('yaa_ui') || '{}');
        settings.baseURL = st.baseURL || '';
        settings.token = st.token || '';
        settings.theme = st.theme || 'light';
      } catch (e) { /* ignore */ }
      applyTheme();
    }
    function saveSettings() {
      try {
        localStorage.setItem('yaa_ui', JSON.stringify({
          baseURL: settings.baseURL, token: settings.token, theme: settings.theme,
        }));
      } catch (e) { /* ignore */ }
      applyTheme();
    }
    async function saveAndReconnect() {
      saveSettings();
      settingsOpen.value = false;
      notify('设置已保存，重新连接…', 'info');
      closeSSE();
      if (currentTurnAbort) currentTurnAbort.abort();
      bootPromise = boot();
      await bootPromise;
    }
    function toggleTheme() {
      settings.theme = settings.theme === 'dark' ? 'light' : 'dark';
      saveSettings();
    }
    function applyTheme() {
      document.documentElement.classList.toggle('dark', settings.theme === 'dark');
    }

    // ---------- 启动 ----------
    async function boot() {
      loadSettings();
      applyTheme();
      const healthPromise = api('/api/v1/health').catch(() => null);
      const agentsPromise = api('/api/v1/agents?page_size=100').catch(e => ({ error: e }));
      const providersPromise = api('/api/v1/providers').catch(() => null);
      const [health, agentsRes, providersRes] = await Promise.all([healthPromise, agentsPromise, providersPromise]);
      connected.value = !!(health && health.ready);
      if (agentsRes && agentsRes.error) {
        notify('加载 Agents 失败：' + agentsRes.error.message, 'error');
      } else {
        agents.value = (agentsRes && agentsRes.items) || [];
      }
      if (providersRes) providers.value = providersRes.items || [];
      booted.value = true;
      if (agents.value.length) {
        agentId.value = agents.value[0].id;
        await onAgentChange();
      }
      pollHealth();
    }

    let healthTimer = null;
    async function pollHealth() {
      const tick = async () => {
        const h = await api('/api/v1/health').catch(() => null);
        connected.value = !!(h && h.ready);
      };
      await tick();
      healthTimer = setInterval(tick, 15000);
    }

    // ---------- Agent / Session ----------
    async function onAgentChange() {
      liveReset();
      closeSSE();
      currentTurnAbort && currentTurnAbort.abort();
      sessionId.value = '';
      messages.value = [];
      if (!agentId.value) return;
      try {
        const d = await api(`/api/v1/agents/${encodeURIComponent(agentId.value)}/sessions?page_size=100`);
        sessions.value = d.items || [];
      } catch (e) {
        sessions.value = [];
        notify('加载会话失败：' + e.message, 'error');
      }
    }

    async function loadSessions() {
      if (!agentId.value) return;
      try {
        const d = await api(`/api/v1/agents/${encodeURIComponent(agentId.value)}/sessions?page_size=100`);
        sessions.value = d.items || [];
      } catch (e) { /* ignore */ }
    }

    async function newChat() {
      if (!agentId.value) {
        notify('请先选择 Agent', 'warning');
        return;
      }
      if (streaming.value) { notify('请等待当前生成完成', 'warning'); return; }
      try {
        const d = await api(`/api/v1/agents/${encodeURIComponent(agentId.value)}/sessions`, {
          method: 'POST',
          body: JSON.stringify({ metadata: { source: 'webui' } }),
        });
        await onAgentChange();
        sessionId.value = d.id;
        await openSession(d.id);
      } catch (e) {
        notify('新建会话失败：' + e.message, 'error');
      }
    }

    async function selectSession(id) {
      if (streaming.value && id !== sessionId.value) { notify('请等待当前生成完成', 'warning'); return; }
      if (id === sessionId.value) return;
      currentTurnAbort && currentTurnAbort.abort();
      sessionId.value = id;
      await openSession(id);
    }

    async function openSession(id) {
      liveReset();
      closeSSE();
      messages.value = [];
      await loadMessages(id);
      openSSE(id);
      await nextTick();
      scrollBottom(true);
    }

    async function loadMessages(id) {
      if (!id) return;
      try {
        const d = await api(`/api/v1/sessions/${encodeURIComponent(id)}/messages?page_size=200`);
        messages.value = d.items || [];
      } catch (e) {
        notify('加载消息失败：' + e.message, 'error');
      }
    }

    async function sessionCmd(cmd, s) {
      try {
        if (cmd === 'delete') {
          if (!confirm(`确定删除会话 ${sessionTitle(s)}？此操作不可恢复。`)) return;
          await api(`/api/v1/sessions/${encodeURIComponent(s.id)}`, { method: 'DELETE' });
          if (sessionId.value === s.id) { messages.value = []; sessionId.value = ''; }
        } else if (cmd === 'pause' || cmd === 'resume') {
          await api(`/api/v1/sessions/${encodeURIComponent(s.id)}/${cmd}`, { method: 'POST' });
        }
        await loadSessions();
        notify(`会话 ${cmd} 成功`, 'success');
      } catch (e) {
        notify(`操作失败：${e.message}`, 'error');
      }
    }

    // ---------- SSE 订阅 ----------
    function openSSE(id) {
      if (!id) return;
      closeSSE();
      sseOk.value = false;
      const ctrl = new AbortController();
      sseAbort = ctrl;
      const run = async () => {
        try {
          const res = await fetch(apiBase() + `/api/v1/sessions/${encodeURIComponent(id)}/events`, {
            headers: headers({ Accept: 'text/event-stream' }),
            signal: ctrl.signal,
          });
          if (!res.ok || !res.body) {
            if (!ctrl.signal.aborted) { sseOk.value = false; scheduleSSEReconnect(id); }
            return;
          }
          sseOk.value = true;
          const reader = res.body.getReader();
          const dec = new TextDecoder();
          let buf = '';
          for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            buf += dec.decode(value, { stream: true });
            let idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              const block = buf.slice(0, idx);
              buf = buf.slice(idx + 2);
              handleSSEBlock(block);
            }
          }
        } catch (e) {
          if (!ctrl.signal.aborted) { sseOk.value = false; scheduleSSEReconnect(id); }
        }
      };
      run();
    }

    function scheduleSSEReconnect(id) {
      clearTimeout(sseReconnectTimer);
      if (sseAbort !== null && id !== sessionId.value) return;
      sseReconnectTimer = setTimeout(() => {
        if (sessionId.value === id && !document.hidden) openSSE(id);
      }, 3000);
    }

    function closeSSE() {
      clearTimeout(sseReconnectTimer);
      if (sseAbort) { sseAbort.abort(); sseAbort = null; }
      sseOk.value = false;
    }

    function handleSSEBlock(block) {
      let data = '';
      for (const line of block.split('\n')) {
        if (line.startsWith('data:')) {
          const v = line.slice(5).trimStart();
          data += v;
        }
      }
      if (!data) return;
      let frame;
      try { frame = JSON.parse(data); } catch (e) { return; }
      switch (frame.type) {
        case 'queued': break;
        case 'assistant_start':
          if (!live.turnId || frame.turn_id === live.turnId) live.done = false;
          break;
        case 'reasoning_delta':
          if (frame.turn_id === live.turnId) live.reasoning += frame.delta || '';
          break;
        case 'assistant_delta':
          if (frame.turn_id === live.turnId) {
            live.text += frame.delta || '';
            scheduleMarkdown();
          }
          break;
        case 'tool_call':
          if (frame.turn_id === live.turnId) {
            const tc = frame.tool_call || {};
            live.tools.push({
              id: tc.id, name: (tc.function && tc.function.name) || 'tool',
              arguments: (tc.function && tc.function.arguments) || '',
              done: false, is_error: false, statusText: '执行中…', open: false,
              tool_call_id: tc.id, content: '',
            });
          }
          break;
        case 'tool_result':
          if (frame.turn_id === live.turnId) {
            const r = frame.tool_result || {};
            const t = live.tools.find(x => x.tool_call_id === r.tool_call_id);
            if (t) {
              t.done = true;
              t.is_error = !!r.is_error;
              t.statusText = (r.is_error ? '执行失败：' : '') + truncate(r.content || '', 400);
              t.content = r.content || '';
            }
          }
          break;
        case 'assistant_done':
          if (!live.turnId || frame.turn_id === live.turnId) {
            live.done = true;
            if (frame.usage) lastUsage.value = frame.usage;
            streaming.value = false;
            currentTurnAbort = null;
            liveResetTools();
            // 以服务端已提交的消息为准重载，保证一致性
            const sid = sessionId.value;
            if (sid) { loadMessages(sid); loadSessions(); }
          }
          break;
        case 'error':
          if (!live.turnId || frame.turn_id === live.turnId) {
            streaming.value = false;
            currentTurnAbort = null;
            if (frame.code === 'canceled') {
              live.error = '已取消';
            } else {
              live.error = frame.message || '生成失败';
            }
            live.done = true;
          }
          break;
        case 'session_end':
          closeSSE();
          break;
      }
    }

    // ---------- 消息分组 ----------
    const messageGroups = computed(() => {
      const groups = [];
      let g = null;
      for (const m of messages.value) {
        if (m.role === 'user') {
          g = { key: m.id, userMessages: [m], assistantMessage: null, live: false, toolCalls: [], error: '', reasonOpen: false, reasoning: '', id: m.id };
          groups.push(g);
        } else if (m.role === 'assistant') {
          const toolCalls = (m.tool_calls || []).map(tc => ({
            id: tc.id, tool_call_id: tc.id, name: (tc.function && tc.function.name) || 'tool',
            arguments: (tc.function && tc.function.arguments) || '',
            done: true, is_error: !!m.is_error, statusText: '', open: false, content: '',
          }));
          if (g && !g.assistantMessage) {
            g.assistantMessage = m;
            g.toolCalls = toolCalls;
            g.reasoning = m.reasoning_content || '';
          } else {
            g = { key: m.id, userMessages: [], assistantMessage: m, live: false, toolCalls: toolCalls, error: '', reasonOpen: false, reasoning: m.reasoning_content || '', id: m.id };
            groups.push(g);
          }
        } else if (m.role === 'tool') {
          // 关联到最近的助手 tool_call
          if (g && g.toolCalls.length) {
            const t = g.toolCalls.find(tc => tc.tool_call_id === m.tool_call_id) || g.toolCalls[g.toolCalls.length - 1];
            t.done = true;
            t.is_error = !!m.is_error;
            t.statusText = truncate(m.content || '', 400);
            t.content = m.content || '';
          }
        }
      }
      // 追加进行中的 turn
      if (streaming.value && live.turnId) {
        groups.push({
          key: 'live-' + live.turnId, userMessages: [], assistantMessage: null,
          live: true, toolCalls: live.tools || [], error: live.error || '',
          reasonOpen: false, reasoning: live.reasoning || '', id: 'live',
          liveText: live.text,
        });
      }
      return groups;
    });

    function mdOfGroup(g) {
      const text = g.live && g.liveText != null ? g.liveText : (g.assistantMessage ? g.assistantMessage.content : '');
      if (!text) return '';
      const html = marked.parse(text);
      return DOMPurify.sanitize(html);
    }

    function reasoningText(g) {
      return (g.live && g.liveText != null) ? live.reasoning : (g.reasoning || '');
    }

    function scheduleMarkdown() {
      cancelAnimationFrame(mdRaf);
      mdRaf = requestAnimationFrame(() => {
        scrollBottom(false);
        mdRaf = 0;
      });
    }

    // ---------- 发送 ----------
    async function send() {
      if (!sessionId.value || !draft.value.trim() || streaming.value) return;
      const content = draft.value.trim();
      draft.value = '';
      const turnId = newTurnId();
      liveReset();
      live.turnId = turnId;
      streaming.value = true;
      liveResetTools();
      // 先把用户消息插进本地列表（REST POST 前 SSE 可能先到）
      const sessionIdNow = sessionId.value;
      const ctrl = new AbortController();
      currentTurnAbort = ctrl;
      try {
        const post = api(`/api/v1/sessions/${encodeURIComponent(sessionIdNow)}/messages`, {
          method: 'POST',
          body: JSON.stringify({ turn_id: turnId, content }),
          signal: ctrl.signal,
        });
        // 即使 SSE 未订阅，也先本地渲染用户消息
        messages.value.push({
          id: 'tmp-' + uuidShort(), session_id: sessionIdNow, role: 'user', content,
          tool_calls: [], tool_call_id: '', created_at: new Date().toISOString(),
        });
        scrollBottom(true);
        await post; // 等待 turn 完成（SSE 已实时推进）
      } catch (e) {
        streaming.value = false;
        currentTurnAbort = null;
        live.done = true;
        if (e.name !== 'AbortError') {
          live.error = e.message || '发送失败';
          notify('发送失败：' + e.message, 'error');
        } else {
          live.error = '已取消';
        }
      }
    }

    function stopGenerate() {
      if (currentTurnAbort) currentTurnAbort.abort();
      // 通过 abort fetch 取消 turn；SSE 会收到 error(code=canceled)
      setTimeout(() => { streaming.value = false; }, 300);
    }

    // ---------- UI 事件 ----------
    function onKeydown(e) {
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
        e.preventDefault();
        if (streaming.value) return;
        send();
      }
    }

    function onScroll() {
      // 自动滚动到顶部时停止吸底（预留）
    }

    function quickSend(text) {
      draft.value = text;
      inputBox.value && inputBox.value.focus();
      if (currentSession.value) send();
      else if (!sessionId.value) { newChat().then(() => setTimeout(send, 200)); }
    }

    function scrollBottom(force) {
      const el = msgBox.value;
      if (!el) return;
      if (force) { el.scrollTop = el.scrollHeight; return; }
      const near = el.scrollHeight - el.scrollTop - el.clientHeight < 160;
      if (near) el.scrollTop = el.scrollHeight;
    }

    function liveReset() {
      live.turnId = ''; live.text = ''; live.reasoning = ''; live.tools = []; live.done = false; live.error = '';
    }
    function liveResetTools() {
      live.tools = [];
    }

    // ---------- 图标注册 ----------
    // (在 mount 前完成；Element Plus 图标是独立包，需逐个注册为全局组件)

    // ---------- 生命周期 ----------
    onMounted(async () => {
      bootPromise = boot();
      await bootPromise;
    });
    watch(streaming, v => {
      if (v) scrollBottom(true);
    });

    return {
      booted, connected, streaming, sseOk, agents, providers, agentId, sessions, sessionId,
      messages, draft, settingsOpen, settings, lastUsage, msgBox, inputBox, mdBox,
      currentAgent, currentSession, canSend, messageGroups, hints,
      onAgentChange, newChat, selectSession, sessionCmd, sessionTitle,
      send, stopGenerate, onKeydown, onScroll, quickSend,
      saveSettings, saveAndReconnect, toggleTheme,
      prettyJSON, statusLabel, mdOfGroup, reasoningText,
    };
  },
});

// Element Plus 图标独立包：mount 前逐个注册为全局组件
if (window.ElementPlusIconsVue) {
  for (const [name, comp] of Object.entries(window.ElementPlusIconsVue)) {
    app.component(name, comp);
  }
}

app.use(ElementPlus).mount('#app');
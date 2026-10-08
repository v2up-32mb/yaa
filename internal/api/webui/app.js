/* Yaa! WebUI —— ChatGPT / Chatbox 风格对话控制台
 * 依赖（本地 vendor，离线可用）：Vue3 + Element Plus + Icons + marked + DOMPurify
 * 传输：SSE 订阅增量 + REST POST 触发 turn（token 经 Authorization 头）
 */
const { createApp, ref, reactive, computed, watch, nextTick, onMounted } = Vue;

// Element Plus UMD 下 ElMessage/ElMessageBox 挂在 ElementPlus 命名空间（非裸全局）
const ElMessage = (window.ElementPlus && window.ElementPlus.ElMessage) || null;
const ElMessageBox = (window.ElementPlus && window.ElementPlus.ElMessageBox) || null;

marked.setOptions({ gfm: true, breaks: false });

let bootPromise = null;

const app = createApp({
  setup() {
    // ---------- 状态 ----------
    const booted = ref(false);
    const connected = ref(false);
    const streaming = ref(false);
    const creating = ref(false); // 新建会话进行中
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
    // 服务端配置查看（GET /api/v1/config，脱敏只读；修改走配置文件 + 重启/热更新）
    const serverCfg = ref(null);
    const serverCfgLoading = ref(false);
    const serverCfgError = ref('');
    const serverCfgOpen = ref([]);
    const lastUsage = ref(null);
    const msgBox = ref(null);
    const inputBox = ref(null);
    const mdBox = ref(null);

    // 会话级模型："provider/model"，空表示用 Agent 默认模型。
    const sessionModelKey = ref('');
    const modelOptions = computed(() => {
      const out = [];
      for (const p of providers.value || []) {
        for (const m of p.models || []) {
          const mid = (m && typeof m === 'object') ? m.id : m;
          if (p.id && mid) out.push({ value: p.id + '/' + mid, provider: p.id, model: mid, label: p.id + ' / ' + mid });
        }
      }
      return out;
    });
    const effectiveModelLabel = computed(() => {
      if (sessionModelKey.value) return sessionModelKey.value;
      if (currentAgent.value && currentAgent.value.model) return currentAgent.value.provider + ' / ' + currentAgent.value.model;
      return '';
    });
    function syncSessionModel(s) {
      sessionModelKey.value = (s && s.model && s.model.provider && s.model.model)
        ? s.model.provider + '/' + s.model.model : '';
    }
    async function onModelChange(val) {
      if (!sessionId.value) { sessionModelKey.value = ''; return; }
      let body = {};
      if (val) {
        const i = val.indexOf('/');
        body = { provider: val.slice(0, i), model: val.slice(i + 1) };
      }
      try {
        const d = await api(`/api/v1/sessions/${encodeURIComponent(sessionId.value)}/model`, {
          method: 'POST', body: JSON.stringify(body),
        });
        const idx = sessions.value.findIndex(x => x.id === sessionId.value);
        if (idx >= 0) sessions.value[idx].model = d.model || null;
        syncSessionModel(idx >= 0 ? sessions.value[idx] : null);
        notify(val ? ('已切换模型：' + val) : '已清除会话模型，改用 Agent 默认', 'success');
      } catch (e) {
        notify('切换模型失败：' + e.message, 'error');
        syncSessionModel(currentSession.value);
      }
    }

    // 当前 turn 的实时状态
    const live = reactive({ turnId: '', text: '', reasoning: '', tools: [], done: false, error: '' });
    // 已结束但未持久化的提示（取消/错误/部分输出），随消息区渲染
    const finalizedLive = ref([]);

    let sseAbort = null;      // 当前 SSE AbortController
    let sseReconnectTimer = null;
    let sseNotified401 = false;
    let sseRetry = 0;
    let currentTurnAbort = null;
    let mdRaf = 0;
    let finalizedTurnId = '';

    // ---------- 派生 ----------
    const currentAgent = computed(() => agents.value.find(a => a.id === agentId.value) || null);
    const currentSession = computed(() => sessions.value.find(s => s.id === sessionId.value) || null);
    const canSend = computed(() => !!sessionId.value && !!draft.value.trim() && !streaming.value && !creating.value);

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
      if (!ElMessage) return;
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

    function sessionStateLabel(s) {
      return { active: '进行中', paused: '已暂停', closed: '已关闭', created: '新会话' }[s] || s;
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
      stopHealthPoll();
      closeSSE();
      if (currentTurnAbort) currentTurnAbort.abort();
      bootPromise = boot();
      await bootPromise;
    }
    function toggleTheme() {
      settings.theme = settings.theme === 'dark' ? 'light' : 'dark';
      saveSettings();
    }
    async function loadServerConfig() {
      serverCfgLoading.value = true;
      serverCfgError.value = '';
      try {
        serverCfg.value = await api('/api/v1/config');
        serverCfgOpen.value = serverCfg.value ? Object.keys(serverCfg.value).slice(0, 1) : [];
      } catch (e) {
        serverCfg.value = null;
        serverCfgError.value = e.message || '加载失败';
      } finally {
        serverCfgLoading.value = false;
      }
    }
    function prettyCfg(v) {
      try { return JSON.stringify(v, null, 2); } catch (e) { return String(v); }
    }
    function cloneDoc() {
      return JSON.parse(JSON.stringify(serverCfg.value || {}));
    }
    // ---------- 在线改配置 ----------
    const cfgTab = ref('conn');
    const cfgSaving = ref(false);
    const cfgSaveResult = ref(null); // {applied, restart_required, paths, error}
    const provFormOpen = ref(false);
    const provEditingId = ref('');
    const provForm = reactive({
      id: '', type: 'openai', base_url: '', api_key: '', timeout: '60s',
      max_retries: 2, retry_interval: '2s', extraText: '', models: [],
    });
    const thinkEffortOptions = ['low', 'medium', 'high', 'max'];
    function blankProvModel() {
      return {
        id: '', name: '', context_window: 32768, max_output: 8192,
        supports_tools: true, supports_vision: false, supports_streaming: true,
        supports_thinking: false, thinking_efforts: [], min_thinking_budget: 0,
      };
    }
    const agentFormOpen = ref(false);
    const agentEditingId = ref('');
    const agentForm = reactive({ id: '', name: '', provider: '', model: '', max_tokens: 4096, temperature: '', system_prompt: '' });
    const cfgProviders = computed(() => (serverCfg.value && serverCfg.value.providers) || []);
    const cfgAgents = computed(() => (serverCfg.value && serverCfg.value.agents) || []);
    const cfgLogLevel = computed({
      get: () => (serverCfg.value && serverCfg.value.log && serverCfg.value.log.level) || 'info',
      set: (v) => { if (serverCfg.value) { serverCfg.value.log = serverCfg.value.log || {}; serverCfg.value.log.level = v; } },
    });
    async function ensureServerCfg() {
      if (!serverCfg.value && !serverCfgLoading.value) await loadServerConfig();
    }
    // PUT 全量文档：成功后刷新服务端快照 + agents/providers 目录。
    async function putConfigDoc(doc, okMsg) {
      cfgSaving.value = true;
      cfgSaveResult.value = null;
      try {
        const r = await api('/api/v1/config', { method: 'PUT', body: JSON.stringify(doc) });
        cfgSaveResult.value = r;
        await loadServerConfig();
        await bootCatalogs();
        if (r.restart_required) {
          notify('已写入文件，以下改动需重启生效：' + (r.paths || []).join(', '), 'warning');
        } else {
          notify(okMsg || '已生效', 'success');
        }
        return true;
      } catch (e) {
        cfgSaveResult.value = { error: e.message };
        notify('保存失败：' + e.message, 'error');
        return false;
      } finally {
        cfgSaving.value = false;
      }
    }
    // 重载 agents/providers 目录并尽量保持当前选择（在线改配置后同步）。
    async function bootCatalogs() {
      const keepAgent = agentId.value;
      const agentsRes = await api('/api/v1/agents?page_size=100').catch(e => ({ error: e }));
      const providersRes = await api('/api/v1/providers').catch(() => null);
      if (!(agentsRes && agentsRes.error)) agents.value = (agentsRes && agentsRes.items) || [];
      if (providersRes) providers.value = providersRes.items || [];
      if (agents.value.length && !agents.value.find(a => a.id === keepAgent)) {
        agentId.value = agents.value[0].id;
        await onAgentChange();
      } else if (!agents.value.length) {
        agentId.value = '';
        sessions.value = [];
        sessionId.value = '';
        sessionModelKey.value = '';
      }
    }
    function openProvForm(p) {
      provEditingId.value = (p && p.id) || '';
      provForm.id = (p && p.id) || '';
      provForm.type = (p && p.type) || 'openai';
      provForm.base_url = (p && p.base_url) || '';
      provForm.api_key = '';
      provForm.timeout = (p && p.timeout) || '60s';
      provForm.max_retries = (p && p.max_retries !== undefined && p.max_retries !== null) ? p.max_retries : 2;
      provForm.retry_interval = (p && p.retry_interval) || '2s';
      try {
        provForm.extraText = (p && p.extra && Object.keys(p.extra).length) ? JSON.stringify(p.extra, null, 2) : '';
      } catch (e) { provForm.extraText = ''; }
      provForm.models = ((p && p.models) || []).map(m => ({
        id: m.id || '', name: m.name || '',
        context_window: m.context_window || 0, max_output: m.max_output || 0,
        supports_tools: !!m.supports_tools, supports_vision: !!m.supports_vision,
        supports_streaming: m.supports_streaming !== false, supports_thinking: !!m.supports_thinking,
        thinking_efforts: (m.thinking_efforts || []).slice(),
        min_thinking_budget: m.min_thinking_budget || 0,
      }));
      if (!provForm.models.length) provForm.models.push(blankProvModel());
      provFormOpen.value = true;
    }
    function addProvModel() { provForm.models.push(blankProvModel()); }
    function delProvModel(i) { provForm.models.splice(i, 1); }
    function parseProvExtra() {
      const t = (provForm.extraText || '').trim();
      if (!t) return { ok: true, value: undefined };
      try {
        const v = JSON.parse(t);
        if (!v || typeof v !== 'object' || Array.isArray(v)) return { ok: false };
        return { ok: true, value: v };
      } catch (e) { return { ok: false }; }
    }
    async function saveProvider() {
      if (!provForm.id.trim()) { notify('请填写提供商 ID', 'warning'); return; }
      if (!provEditingId.value && cfgProviders.value.find(p => p.id === provForm.id.trim())) {
        notify('ID 已存在', 'warning'); return;
      }
      const models = [];
      for (const m of provForm.models) {
        if (!m.id.trim()) { notify('模型 ID 不能为空', 'warning'); return; }
        const e = {
          id: m.id.trim(), name: (m.name || '').trim(),
          context_window: Number(m.context_window) || 0, max_output: Number(m.max_output) || 0,
          supports_tools: !!m.supports_tools, supports_vision: !!m.supports_vision,
          supports_streaming: !!m.supports_streaming, supports_thinking: !!m.supports_thinking,
          thinking_efforts: (m.thinking_efforts || []).slice(),
          min_thinking_budget: Number(m.min_thinking_budget) || 0,
        };
        if (!e.supports_thinking) { e.thinking_efforts = []; e.min_thinking_budget = 0; }
        models.push(e);
      }
      if (!models.length) { notify('至少添加一个模型', 'warning'); return; }
      const extra = parseProvExtra();
      if (!extra.ok) { notify('附加请求体必须是 JSON 对象', 'warning'); return; }
      const doc = cloneDoc();
      const entry = {
        id: provForm.id.trim(), type: provForm.type, base_url: provForm.base_url.trim(),
        timeout: (provForm.timeout || '').trim() || '60s',
        max_retries: Number(provForm.max_retries),
        retry_interval: (provForm.retry_interval || '').trim() || '2s',
        models: models,
      };
      if (extra.value !== undefined) entry.extra = extra.value;
      // api_key：空=保持不变（*** 占位由后端合并）；新建时空=不设置。
      // 明文与 ${ENV} 引用都允许（openai 等类型要求非空，后端校验兜底）。
      if (provForm.api_key) {
        entry.api_key = provForm.api_key;
      } else if (provEditingId.value) {
        entry.api_key = '***';
      }
      doc.providers = doc.providers || [];
      if (provEditingId.value) {
        const i = doc.providers.findIndex(p => p.id === provEditingId.value);
        if (i >= 0) {
          // 编辑：保留表单未覆盖的老字段（extra 等）。
          doc.providers[i] = Object.assign({}, doc.providers[i], entry);
        }
      } else {
        doc.providers.push(entry);
      }
      if (await putConfigDoc(doc, '提供商已保存')) provFormOpen.value = false;
    }
    async function deleteProvider(p) {
      const ok = await confirmDialog(`确定删除提供商「${p.id}」？引用它的 Agent 会校验失败。`, '删除提供商');
      if (!ok) return;
      const doc = cloneDoc();
      doc.providers = (doc.providers || []).filter(x => x.id !== p.id);
      await putConfigDoc(doc, '提供商已删除');
    }
    function openAgentForm(a) {
      agentEditingId.value = (a && a.id) || '';
      agentForm.id = (a && a.id) || '';
      agentForm.name = (a && a.name) || '';
      agentForm.provider = (a && a.provider) || ((cfgProviders.value[0] && cfgProviders.value[0].id) || '');
      agentForm.model = (a && a.model) || '';
      agentForm.max_tokens = (a && a.max_tokens) || 4096;
      agentForm.temperature = (a && a.temperature !== undefined && a.temperature !== null) ? String(a.temperature) : '';
      agentForm.system_prompt = (a && a.system_prompt) || '';
      agentFormOpen.value = true;
    }
    function agentModelsFor(providerId) {
      const p = cfgProviders.value.find(x => x.id === providerId);
      return ((p && p.models) || []).map(m => m.id);
    }
    async function saveAgent() {
      if (!agentForm.id.trim()) { notify('请填写 Agent ID', 'warning'); return; }
      if (!agentForm.name.trim()) { notify('请填写名称', 'warning'); return; }
      if (!agentForm.provider) { notify('请选择提供商', 'warning'); return; }
      if (!agentEditingId.value && cfgAgents.value.find(a => a.id === agentForm.id.trim())) {
        notify('ID 已存在', 'warning'); return;
      }
      const doc = cloneDoc();
      const entry = {
        id: agentForm.id.trim(), name: agentForm.name.trim(),
        provider: agentForm.provider, model: agentForm.model.trim(),
        system_prompt: agentForm.system_prompt,
        tools: [], skills: [],
        max_tokens: Number(agentForm.max_tokens) || 4096,
      };
      if (agentForm.temperature !== '') entry.temperature = Number(agentForm.temperature);
      doc.agents = doc.agents || [];
      if (agentEditingId.value) {
        const i = doc.agents.findIndex(a => a.id === agentEditingId.value);
        if (i >= 0) doc.agents[i] = Object.assign({}, doc.agents[i], entry);
      } else {
        doc.agents.push(entry);
      }
      if (await putConfigDoc(doc, 'Agent 已保存')) agentFormOpen.value = false;
    }
    async function deleteAgent(a) {
      const ok = await confirmDialog(`确定删除 Agent「${a.id}」？其会话历史保留但无法再建新会话。`, '删除 Agent');
      if (!ok) return;
      const doc = cloneDoc();
      doc.agents = (doc.agents || []).filter(x => x.id !== a.id);
      await putConfigDoc(doc, 'Agent 已删除');
    }
    async function saveLogLevel() {
      const doc = cloneDoc();
      doc.log = doc.log || {};
      doc.log.level = cfgLogLevel.value;
      await putConfigDoc(doc, '日志级别已生效');
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
    function stopHealthPoll() {
      if (healthTimer) { clearInterval(healthTimer); healthTimer = null; }
    }
    async function pollHealth() {
      stopHealthPoll();
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
      sessionModelKey.value = '';
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
      if (streaming.value || creating.value) { notify('请等待当前操作完成', 'warning'); return; }
      creating.value = true;
      try {
        const d = await api(`/api/v1/agents/${encodeURIComponent(agentId.value)}/sessions`, {
          method: 'POST',
          body: JSON.stringify({ metadata: { source: 'webui' } }),
        });
        await onAgentChange();
        sessionId.value = d.id;
        syncSessionModel(d);
        await openSession(d.id);
      } catch (e) {
        notify('新建会话失败：' + e.message, 'error');
      } finally {
        creating.value = false;
      }
    }

    async function selectSession(id) {
      if (streaming.value && id !== sessionId.value) { notify('请等待当前生成完成', 'warning'); return; }
      if (id === sessionId.value) return;
      if (currentTurnAbort) currentTurnAbort.abort();
      sessionId.value = id;
      syncSessionModel(sessions.value.find(x => x.id === id) || null);
      await openSession(id);
    }

    async function openSession(id) {
      liveReset();
      finalizedLive.value = [];
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
          const ok = await confirmDialog(`确定删除会话「${sessionTitle(s)}」？此操作不可恢复。`, '删除会话');
          if (!ok) return;
          await api(`/api/v1/sessions/${encodeURIComponent(s.id)}`, { method: 'DELETE' });
          if (sessionId.value === s.id) {
            messages.value = [];
            sessionId.value = '';
            closeSSE();
          }
        } else if (cmd === 'pause' || cmd === 'resume') {
          await api(`/api/v1/sessions/${encodeURIComponent(s.id)}/${cmd}`, { method: 'POST' });
          if (sessionId.value === s.id) {
            // 本会话状态变化，同步列表中的 DTO
            const idx = sessions.value.findIndex(x => x.id === s.id);
            if (idx >= 0) sessions.value[idx].state = (cmd === 'pause' ? 'paused' : 'active');
          }
        }
        await loadSessions();
        notify(cmd === 'delete' ? '会话已删除' : `会话${cmd === 'pause' ? '已暂停' : '已恢复'}`, 'success');
      } catch (e) {
        notify(`操作失败：${e.message}`, 'error');
      }
    }

    function confirmDialog(message, title = '确认') {
      if (!ElMessageBox) {
        return Promise.resolve(window.confirm(message));
      }
      return new Promise(resolve => {
        ElMessageBox.confirm(message, title, {
          confirmButtonText: '确定', cancelButtonText: '取消', type: 'warning',
        }).then(() => resolve(true)).catch(() => resolve(false));
      });
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
            if (!ctrl.signal.aborted) {
              if (res.status === 401 || res.status === 403) {
                // 认证失败不需要无限重连：提示一次，等待设置
                if (!sseNotified401) {
                  sseNotified401 = true;
                  notify('流式订阅被拒绝（401/403）：请检查设置里的 Token', 'error');
                }
                sseOk.value = false;
              } else {
                sseOk.value = false;
                scheduleSSEReconnect(id);
              }
            }
            return;
          }
          sseNotified401 = false;
          sseRetry = 0;
          sseOk.value = true;
          const reader = res.body.getReader();
          const dec = new TextDecoder();
          let buf = '';
          for (;;) {
            const { done, value } = await reader.read();
            if (done) break; // 服务端关闭连接（非本端 abort）→ 外层重连
            buf += dec.decode(value, { stream: true });
            // 统一 \r\n 与 \n（SSE 规范两者皆可）
            buf = buf.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
            let idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              const block = buf.slice(0, idx);
              buf = buf.slice(idx + 2);
              handleSSEBlock(block);
            }
          }
          if (!ctrl.signal.aborted) {
            sseOk.value = false;
            scheduleSSEReconnect(id);
          }
        } catch (e) {
          if (!ctrl.signal.aborted) { sseOk.value = false; scheduleSSEReconnect(id); }
        }
      };
      run();
    }

    function scheduleSSEReconnect(id) {
      clearTimeout(sseReconnectTimer);
      if (sseAbort === null || id !== sessionId.value) return; // 主动关闭或已切换
      // 指数退避：1s → 2s → 4s … 封顶 15s
      const delay = Math.min(15000, 1000 * Math.pow(2, sseRetry));
      sseRetry = Math.min(sseRetry + 1, 6);
      sseReconnectTimer = setTimeout(() => {
        if (sessionId.value === id && !document.hidden) openSSE(id);
      }, delay);
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
            finalizeLiveTurn();
            live.done = true;
            liveResetTools();
            const esid = sessionId.value;
            if (esid) { loadMessages(esid); loadSessions(); }
          }
          break;
        case 'session_end':
          closeSSE();
          break;
      }
    }

    // 把已结束但未持久化的 turn 内容转成列表项保留（取消/错误/部分输出）。
    // 同一个 turn 只收尾一次（SSE error 帧与 REST catch 都可能触发）。
    function finalizeLiveTurn() {
      if (live.turnId && live.turnId === finalizedTurnId) return;
      finalizedTurnId = live.turnId || finalizedTurnId;
      if (live.error) {
        finalizedLive.value.push({ kind: 'error', key: live.turnId || String(Date.now()), text: live.error });
      }
      if (live.text || live.tools.length) {
        finalizedLive.value.push({
          kind: 'partial', key: live.turnId || String(Date.now()),
          text: live.text, reasoning: live.reasoning, tools: live.tools.slice(),
        });
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
      // 已结束但未持久化的内容：错误/取消提示 + 部分输出
      for (const f of finalizedLive.value) {
        if (f.kind === 'error') {
          groups.push({
            key: 'fin-' + f.key, userMessages: [], assistantMessage: null, live: false,
            toolCalls: [], error: f.text, reasonOpen: false, reasoning: '', id: 'fin',
          });
        } else if (f.kind === 'partial') {
          groups.push({
            key: 'fin-' + f.key, userMessages: [], assistantMessage: null, live: false,
            toolCalls: f.tools || [], error: '', reasonOpen: false, reasoning: f.reasoning || '',
            id: 'fin', liveText: f.text, partial: true,
          });
        }
      }
      return groups;
    });

    function mdOfGroup(g) {
      let text = '';
      if (g.live || g.partial) text = g.liveText || '';
      else if (g.assistantMessage) text = g.assistantMessage.content || '';
      if (!text) return '';
      try {
        return DOMPurify.sanitize(marked.parse(text));
      } catch (e) {
        return '';
      }
    }

    function reasoningText(g) {
      if (g.live && g.liveText != null) return live.reasoning;
      return g.reasoning || '';
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
      if (!sessionId.value) {
        notify(creating.value ? '会话创建中，请稍候…' : '请先选择或新建一个会话', 'warning');
        return;
      }
      if (!currentSession.value) {
        notify('会话已失效，请重新选择', 'warning');
        return;
      }
      if (!effectiveModelLabel.value) {
        notify('请先在顶部选择本会话使用的模型', 'warning');
        return;
      }
      if (!draft.value.trim() || streaming.value) return;
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
      messages.value.push({
        id: 'tmp-' + uuidShort(), session_id: sessionIdNow, role: 'user', content,
        tool_calls: [], tool_call_id: '', created_at: new Date().toISOString(),
      });
      scrollBottom(true);
      try {
        await api(`/api/v1/sessions/${encodeURIComponent(sessionIdNow)}/messages`, {
          method: 'POST',
          body: JSON.stringify({ turn_id: turnId, content }),
          signal: ctrl.signal,
        });
        // POST 返回即 turn 已提交。若 SSE 没有及时把 assistant_done 送达到前端
        // （订阅断开/丢帧），这里兜底：以服务端为准重载 + 清流式状态。
        if (streaming.value) {
          streaming.value = false;
          currentTurnAbort = null;
          live.done = true;
          liveResetTools();
        }
        if (sessionIdNow === sessionId.value) {
          await loadMessages(sessionIdNow);
          loadSessions();
        }
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
        finalizeLiveTurn();
        // 清掉本地临时用户消息，回到服务端一致状态
        if (sessionIdNow === sessionId.value) {
          await loadMessages(sessionIdNow);
        } else {
          messages.value = messages.value.filter(m => m.id && !String(m.id).startsWith('tmp-'));
        }
      }
    }

    function stopGenerate() {
      // 取消 REST 请求即取消服务端 turn（handler 用 request context）；
      // SSE 侧会收到 error(code=canceled) 帧并完成收尾。
      if (currentTurnAbort) currentTurnAbort.abort();
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
      if (currentSession.value) {
        send();
      } else if (!sessionId.value) {
        (async () => {
          await newChat();
          send();
        })();
      } else {
        send();
      }
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
      currentAgent, currentSession, canSend, messageGroups, hints, finalizedLive,
      onAgentChange, newChat, selectSession, sessionCmd, sessionTitle,
      sessionModelKey, modelOptions, effectiveModelLabel, onModelChange,
      send, stopGenerate, onKeydown, onScroll, quickSend,
      saveSettings, saveAndReconnect, toggleTheme, confirmDialog,
      serverCfg, serverCfgLoading, serverCfgError, serverCfgOpen, loadServerConfig, prettyCfg,
      cfgTab, cfgSaving, cfgSaveResult, ensureServerCfg, putConfigDoc,
      cfgProviders, cfgAgents, cfgLogLevel, saveLogLevel,
      provFormOpen, provEditingId, provForm, openProvForm, saveProvider, deleteProvider,
      thinkEffortOptions, addProvModel, delProvModel,
      agentFormOpen, agentEditingId, agentForm, openAgentForm, saveAgent, deleteAgent, agentModelsFor,
      prettyJSON, statusLabel, mdOfGroup, reasoningText, sessionStateLabel,
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
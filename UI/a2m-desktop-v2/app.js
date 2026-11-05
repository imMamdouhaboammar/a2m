/* A2M Desktop local control UI.
   Only same-origin /api endpoints are used. No demo data, credentials or commands are fabricated. */
(() => {
  "use strict";

  const VIEWS = ["overview", "terminal", "activity", "access", "settings"];
  const MAX_LINES = 400;
  const MAX_EVENTS = 60;
  const $ = (selector) => document.querySelector(selector);
  const state = {
    status: null,
    sessions: [],
    selectedPid: null,
    followLatest: true,
    lines: new Map(),
    seenSnapshots: new Set(),
    events: [],
    tunnelEvents: [],
    pendingConnection: false,
    pendingAction: null,
    stream: null,
    streamOnline: false
  };
  let toastTimeout;
  let statusErrorShown = false;
  let pendingSnapshot = 0;

  function makeElement(tag, className, value) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (value !== undefined) node.textContent = String(value);
    return node;
  }

  function showToast(message) {
    const node = $("#toast");
    node.textContent = message;
    node.classList.add("visible");
    clearTimeout(toastTimeout);
    toastTimeout = setTimeout(() => node.classList.remove("visible"), 5000);
  }

  function showView(name) {
    if (!VIEWS.includes(name)) return;
    document.querySelectorAll("[data-nav]").forEach((button) => {
      const active = button.dataset.nav === name;
      button.classList.toggle("active", active);
      if (active) button.setAttribute("aria-current", "page");
      else button.removeAttribute("aria-current");
    });
    VIEWS.forEach((viewName) => {
      const node = document.getElementById(`view-${viewName}`);
      node.hidden = viewName !== name;
      node.classList.toggle("active", viewName === name);
    });
    $("#main-content").scrollTop = 0;
    if (name === "settings") void refreshCapabilities();
    if (name === "settings") void refreshCredentialStatus();
  }
  // Used by the native macOS menu to focus the terminal screen.
  window.a2mShowView = showView;

  function timeString(value) {
    if (!value) return "--:--:--";
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "--:--:--" : date.toLocaleTimeString([], { hour12: false });
  }

  function durationString(total) {
    const t = Math.max(0, Math.floor(Number(total) || 0));
    return [Math.floor(t / 3600), Math.floor((t % 3600) / 60), t % 60]
      .map((v) => String(v).padStart(2, "0")).join(":");
  }

  async function api(path, options = {}) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), options.method === "POST" ? 12000 : 5500);
    try {
      const response = await fetch(path, {
        ...options,
        cache: "no-store",
        credentials: "same-origin",
        headers: { Accept: "application/json", ...(options.headers || {}) },
        signal: controller.signal
      });
      if (!response.ok) {
        const body = (await response.text()).trim().slice(0, 600);
        let message = body;
        try {
          const error = JSON.parse(body);
          message = String(error.message || error.error || error.status || body);
        } catch { /* Keep plain-text HTTP errors readable. */ }
        throw new Error(message || `HTTP ${response.status}`);
      }
      return await response.json();
    } finally {
      clearTimeout(timeout);
    }
  }

  function setService(id, status) {
    const element = document.getElementById(id);
    if (!element) return;
    element.textContent = status || "Unknown";
    const isHealthy = /^(running|healthy|reachable)$/i.test(status || "");
    element.classList.toggle("running", isHealthy);
    const dot = document.getElementById(`${id}-dot`) || element.closest(".service-chip")?.querySelector(".indicator") || element.closest(".service-item")?.querySelector(".indicator");
    if (dot) dot.classList.toggle("connected", isHealthy);
  }

  function renderConnection() {
    const data = state.status;
    const available = Boolean(data);

    function renderTunnelSlot(slotNum) {
      const tData = data?.tunnels?.[String(slotNum)] || {};
      const slotRaw = String(tData?.state || "");
      const profileLabel = $(`#slot-tunnel-${slotNum} .tunnel-profile-label`);
      if (profileLabel) profileLabel.textContent = tData.profile || (slotNum === 1 ? "a2m" : "a2m-2");
      const slotReady = Boolean(tData?.ready);
      const slotConnected = slotReady || slotRaw.toLowerCase() === "connected";
      const pending = state.pendingTunnelActions?.[slotNum];
      const slotConnecting = pending === "connect" || slotRaw.toLowerCase() === "connecting";
      const slotDisconnecting = pending === "disconnect";

      const card = $(`#slot-tunnel-${slotNum}`);
      const dot = $(`#tunnel-${slotNum}-dot`);
      const statusText = $(`#tunnel-${slotNum}-status-text`);
      const slotBtn = $(`#tunnel-${slotNum}-button`);
      const duration = $(`#tunnel-${slotNum}-duration`);
      const idEl = $(`#tunnel-${slotNum}-id`);

      if (card) {
        card.classList.toggle("is-connected", slotConnected);
      }
      if (dot) {
        dot.className = `indicator status-indicator ${slotConnected ? "connected" : slotConnecting ? "connecting" : "disconnected"}`;
      }
      if (statusText) {
        statusText.textContent = !available ? "Offline" : slotDisconnecting ? "Disconnecting…" : slotConnecting ? "Connecting…" : slotConnected ? (tData.owner === "external" ? "Connected (Ext)" : "Connected") : "Disconnected";
      }
      if (slotBtn) {
        slotBtn.textContent = !available ? "Offline" : slotDisconnecting ? "Disconnecting…" : slotConnecting ? "Connecting…" : slotConnected ? "Disconnect" : `Connect T${slotNum}`;
        slotBtn.className = `button ${slotConnected ? "button-danger" : "button-primary"}`;
        slotBtn.disabled = !available || slotConnecting || slotDisconnecting;
      }
      if (duration) {
        duration.textContent = slotConnected ? durationString(tData.uptimeSeconds) : "--:--:--";
      }
      if (idEl) {
        if (tData.tunnelId) {
          idEl.textContent = tData.tunnelId.slice(0, 16) + "…";
          idEl.title = tData.tunnelId;
        } else {
          idEl.textContent = slotNum === 1 ? "tunnel_6ab2…" : "tunnel_6ae6…";
        }
      }
    }

    renderTunnelSlot(1);
    renderTunnelSlot(2);

    const calls = data?.tunnel?.mcpToolCalls;
    const callsEl = $("#tunnel-calls-count");
    if (callsEl) {
      callsEl.textContent = Number.isSafeInteger(calls) ? String(calls) : "0";
    }

    const device = data?.device || {};
    const hostname = typeof device.hostname === "string" && device.hostname ? device.hostname : "This Mac";
    const platform = typeof device.platform === "string" && device.platform ? device.platform : "Not reported";
    $("#sidebar-hostname").textContent = hostname;
    $("#sidebar-platform").textContent = platform;
    const version = typeof data?.appVersion === "string" && /^\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$/.test(data.appVersion)
      ? `v${data.appVersion}` : "Unavailable";
    $("#sidebar-app-version").textContent = version;
    $("#sidebar-app-version").title = version === "Unavailable" ? "Running gateway version is not reported" : `Running A2M gateway ${version}`;
    $("#sidebar-status").classList.toggle("connected", available);
    $("#api-connection").textContent = available ? "Gateway online" : "Gateway offline";
    $("#last-updated").textContent = available ? timeString(new Date()) : "--:--";

    const authEnabled = data?.auth?.enabled === true;
    const authKnown = typeof data?.auth?.enabled === "boolean";
    const authLabel = !authKnown ? "Unavailable" : authEnabled ? "Bearer Token Active" : "Local-only: token disabled";
    const authDetail = !authKnown ? "Gateway status is unavailable." : authEnabled
      ? `Gateway role: ${data.auth.role || "not reported"}. This does not verify relay client identity.`
      : "The HTTP endpoint is bound to 127.0.0.1. The tunnel-client manages remote access separately.";
    const authDot = $("#auth-indicator");
    if (authDot) {
      authDot.classList.toggle("connected", authEnabled || authKnown);
    }
    const authEl = $("#auth-summary");
    if (authEl) authEl.textContent = authLabel;
    const authExpl = $("#auth-explanation");
    if (authExpl) authExpl.textContent = authDetail;
    const accessAuth = $("#access-auth");
    if (accessAuth) accessAuth.textContent = authLabel;
    const accessAuthDetail = $("#access-auth-detail");
    if (accessAuthDetail) accessAuthDetail.textContent = authDetail;

    setService("service-gateway", available ? "running" : "unavailable");
    setService("service-fs", data?.sidecars?.fsEngine || "Unknown");
    setService("service-docs", data?.sidecars?.docService || "Unknown");
  }

  async function refreshCapabilities() {
    const stateLabel = $("#capability-state");
    stateLabel.textContent = "Checking gateway";
    try {
      const payload = await api("/api/capabilities");
      const catalog = payload.capabilities || {};
      const graph = catalog.code_review_graph || {};
      const featured = Array.isArray(catalog.featured_capabilities) ? catalog.featured_capabilities : [];
      const featuredList = document.createDocumentFragment();
      const statusLabels = {
        cli_mcp_callable: "CLI callable via MCP", mcp_operator_approved: "MCP approved", node_healthy: "Node healthy", node_unavailable: "Node offline",
        cli_installed_mcp_not_approved: "CLI installed · MCP not approved",
        cli_installed_mcp_not_configured: "CLI installed · MCP not configured",
        mcp_configured_not_approved: "MCP configured · not approved",
        cli_installed: "CLI installed", mcp_not_configured: "MCP not configured", not_installed: "Not installed"
      };
      for (const tool of featured) {
        const row = makeElement("div", "featured-row");
        row.setAttribute("role", "listitem");
        const copy = makeElement("div", "featured-copy");
        copy.append(makeElement("strong", "", String(tool.id || "Unnamed")),
          makeElement("span", "", String(tool.description || "No description")));
        const status = makeElement("span", `featured-status ${(tool.mcp_available || tool.cli_available) ? "ready" : tool.installed ? "" : "unavailable"}`,
          statusLabels[tool.status] || String(tool.status || "Unknown"));
        row.append(copy, status);
        row.title = String(tool.suggested_use || "");
        featuredList.append(row);
      }
      if (!featured.length) featuredList.append(makeElement("div", "featured-row", "No capability catalog available."));
      $("#capability-featured-count").textContent = `${featured.length} TOOLS`;
      $("#capability-featured-list").replaceChildren(featuredList);
      const skills = catalog.local_skills || {};
      const external = catalog.external_mcp || {};
      const graphCount = Number(graph.native_tools) || 0;
      const servers = Array.isArray(payload.mcp_servers?.servers) ? payload.mcp_servers.servers : [];
      $("#capability-skills").textContent = Number.isSafeInteger(skills.count) ? String(skills.count) + " discoverable" : "Not reported";
      $("#capability-graph").textContent = graphCount ? String(graphCount) + " native tools" : "Not enabled";
      $("#capability-graph-note").textContent = graphCount ? "Installed local MCP adapter is ready" : graph.installed ? "Installed, but no approved workspace or adapter" : "Executable not found";
      const activeServers = Number(external.active_servers) || 0;
      const activeTools = Number(external.active_tools) || 0;
      $("#capability-servers-count").textContent = Number.isSafeInteger(external.count) ? String(external.count) + " discovered" : "Not reported";
      $("#capability-servers-note").textContent = activeServers
        ? `${activeServers} approved server(s) providing ${activeTools} native tools`
        : "Each provider and tool requires local operator approval";
      $("#capability-servers-state").textContent = activeServers ? `${activeServers} ACTIVE` : "NONE APPROVED";
      const approvedRepos = Number(graph.approved_repository_count) || 0;
      $("#capability-workspace").textContent = graph.workspace_configured
        ? `${approvedRepos} explicit repository root(s) approved. Each repository gets an isolated graph process and index. Configure capabilityWorkspaces to approve additional exact paths.`
        : "No primary workspace configured. Configure capabilityWorkspace before using approved repositories.";
      const container = $("#capability-server-list");
      const list = document.createDocumentFragment();
      for (const server of servers) {
        const chip = makeElement("span", "capability-server-tag", String(server.name || "Unnamed") + " · " + String(server.state || "disabled"));
        list.append(chip);
      }
      if (!servers.length) list.append(makeElement("span", "", "No configured third-party servers discovered."));
      container.replaceChildren(list);
      stateLabel.textContent = "Live inventory";
    } catch (error) {
      stateLabel.textContent = "Unavailable";
      $("#capability-featured-count").textContent = "UNAVAILABLE";
      $("#capability-featured-list").textContent = "The live capability catalog is unavailable.";
      $("#capability-skills").textContent = "Unavailable";
      $("#capability-graph").textContent = "Unavailable";
      $("#capability-servers-count").textContent = "Unavailable";
      $("#capability-servers-note").textContent = "Approval status unavailable";
      $("#capability-servers-state").textContent = "OFFLINE";
      $("#capability-workspace").textContent = "This gateway version does not expose capability data yet.";
      $("#capability-server-list").textContent = "Capability inventory could not be loaded.";
    }
  }

  async function refreshStatus() {
    try {
      const result = await api("/api/status");
      if (!result || typeof result.connectionState !== "string") throw new Error("Invalid gateway status");
      state.status = result;
      statusErrorShown = false;
    } catch (error) {
      state.status = null;
      if (!statusErrorShown) {
        showToast(`Gateway not reachable: ${error.message}`);
        statusErrorShown = true;
      }
    }
    renderConnection();
  }

  function sessionIsRunning(session) {
    return session && (session.state === "running" || session.state === "waiting_for_input");
  }

  function renderSessionList(id) {
    const target = document.getElementById(id);
    const fragment = document.createDocumentFragment();
    for (const session of state.sessions) {
      const button = makeElement("button", "session-row");
      button.type = "button";
      button.dataset.session = String(session.pid);
      const selected = session.pid === state.selectedPid;
      button.classList.toggle("selected", selected);
      button.setAttribute("aria-pressed", String(selected));
      button.setAttribute("aria-label", `${session.command}, PID ${session.pid}, ${session.state}`);
      const dot = makeElement("span", `indicator${sessionIsRunning(session) ? " connected" : ""}`);
      dot.setAttribute("aria-hidden", "true");
      const details = makeElement("span", "session-row-text");
      details.append(makeElement("strong", "", session.command || "Unknown command"));
      details.append(makeElement("span", "", `PID ${session.pid} · ${session.source === "mcp-worker" ? "MCP worker · client ID not provided" : "Dashboard gateway"}`));
      const meta = makeElement("span", "session-row-meta");
      meta.append(makeElement("span", sessionIsRunning(session) ? "running" : "", session.state || "unknown"));
      meta.append(makeElement("span", "", timeString(session.startedAt)));
      button.append(dot, details, meta);
      button.addEventListener("click", () => selectSession(session.pid, true));
      fragment.append(button);
    }
    if (!state.sessions.length) {
      const empty = makeElement("div", "session-empty-state");
      const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.setAttribute("class", "icon session-empty-icon");
      const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
      use.setAttribute("href", "#i-terminal");
      svg.append(use);
      empty.append(
        svg,
        makeElement("strong", "", "No active processes"),
        makeElement("p", "", "AI-invoked tools will appear here in real time.")
      );
      fragment.append(empty);
    }
    target.replaceChildren(fragment);
  }

  function renderTerminal(id) {
    const target = document.getElementById(id);
    const lines = state.lines.get(state.selectedPid) || [];
    const fragment = document.createDocumentFragment();
    if (state.selectedPid === null) {
      if (state.tunnelEvents.length) {
        fragment.append(makeElement("div", "log-line muted", "Showing tunnel transport events. Select a process above to inspect its terminal output:"));
        for (const event of state.tunnelEvents.slice(-40)) {
          const row = makeElement("div", "log-line stdout");
          row.append(makeElement("span", "log-time", timeString(event.timestamp)));
          row.append(makeElement("span", "log-text", event.operation));
          fragment.append(row);
        }
      } else {
        const welcome = makeElement("div", "terminal-welcome");
        welcome.append(
          makeElement("div", "log-line command", "A2M Desktop Terminal Monitor — Ready"),
          makeElement("div", "log-line stdout", "Listening on local loopback for AI agent tool execution:"),
          makeElement("div", "log-line stdout", "  • Tunnel 1: 127.0.0.1:8080 (Primary)"),
          makeElement("div", "log-line stdout", "  • Tunnel 2: 127.0.0.1:8081 (Secondary)"),
          makeElement("div", "log-line muted", "\nAwaiting process execution from connected AI clients...")
        );
        fragment.append(welcome);
      }
    } else if (!lines.length) {
      fragment.append(makeElement("div", "log-line muted", "No captured output for this process yet."));
    } else {
      for (const line of lines.slice(-MAX_LINES)) {
        const row = makeElement("div", `log-line ${line.stream === "stderr" ? "stderr" : "stdout"}`);
        row.append(makeElement("span", "log-time", timeString(line.timestamp)));
        row.append(makeElement("span", "log-text", line.text));
        fragment.append(row);
      }
    }
    target.replaceChildren(fragment);
    const autoscroll = id === "overview-terminal-output" ? $("#overview-autoscroll") : $("#full-autoscroll");
    if (autoscroll.checked) target.scrollTop = target.scrollHeight;
  }

  function renderSessions() {
    renderSessionList("overview-session-list");
    renderSessionList("full-session-list");
    const current = state.sessions.find((s) => s.pid === state.selectedPid);
    const title = current?.command || (state.tunnelEvents.length ? "Tunnel events · not terminal output" : "Select a session");
    $("#overview-terminal-title").textContent = title;
    $("#full-terminal-title").textContent = title;
    $("#session-count").textContent = String(state.sessions.length);
    const followButton = $("#follow-latest-button");
    if (followButton) {
      followButton.textContent = state.followLatest ? "Following latest" : "Resume following";
      followButton.setAttribute("aria-pressed", String(state.followLatest));
    }
    $("#stop-session").disabled = !sessionIsRunning(current);
    renderTerminal("overview-terminal-output");
    renderTerminal("full-terminal-output");
  }

  async function fetchSnapshot(pid) {
    const token = ++pendingSnapshot;
    try {
      const source = state.sessions.find((session) => session.pid === pid)?.source || "dashboard";
      const response = await api(`/api/output?pid=${encodeURIComponent(pid)}&source=${encodeURIComponent(source)}&limit=250`);
      if (state.selectedPid !== pid || token !== pendingSnapshot) return;
      const snapshot = Array.isArray(response.lines) ? response.lines : [];
      const fromStream = state.lines.get(pid) || [];
      // Keep stream lines captured after the snapshot started, without overwriting them.
      const merged = snapshot.map((row) => ({ timestamp: row.timestamp, stream: row.stream, text: String(row.text || "") }));
      if (fromStream.length) merged.push(...fromStream);
      state.lines.set(pid, merged.slice(-MAX_LINES));
      renderSessions();
    } catch (error) {
      if (state.selectedPid === pid) showToast(`Could not read process output: ${error.message}`);
    }
  }

  function selectSession(pid, manuallySelected = false) {
    state.selectedPid = pid;
    if (manuallySelected) state.followLatest = false;
    renderSessions();
    if (!state.seenSnapshots.has(pid)) {
      state.seenSnapshots.add(pid);
      void fetchSnapshot(pid);
    }
  }

  async function refreshSessions() {
    try {
      const list = await api("/api/sessions");
      if (!Array.isArray(list)) throw new Error("Invalid sessions response");
      state.sessions = list.filter((item) => Number.isSafeInteger(item.pid) && item.pid > 0)
        .sort((a, b) => Date.parse(b.startedAt || 0) - Date.parse(a.startedAt || 0));
      if (state.followLatest && state.sessions.length && state.sessions[0].pid !== state.selectedPid) selectSession(state.sessions[0].pid);
      else if (state.selectedPid === null && state.sessions.length) selectSession(state.sessions[0].pid);
      if (state.selectedPid !== null && !state.sessions.some((s) => s.pid === state.selectedPid)) {
        state.selectedPid = null;
      }
      renderSessions();
    } catch (error) {
      // Preserve the last known sessions during transient gateway outages.
      if (!statusErrorShown) showToast(`Cannot refresh sessions: ${error.message}`);
    }
  }

  function renderActivity() {
    const merged = [
      ...state.events.map((item) => ({
        timestamp: item.timestamp, client: item.source === "mcp-worker" ? "MCP worker · client ID unknown" : "Dashboard gateway",
        operation: item.stream === "stderr" ? "Process stderr" : "Process stdout",
        target: `PID ${item.pid}: ${item.command || "process"}`
      })),
      ...state.tunnelEvents.map((item) => ({
        timestamp: item.timestamp, client: "Tunnel client",
        operation: item.operation, target: "MCP transport event"
      }))
    ].sort((a, b) => Date.parse(b.timestamp || 0) - Date.parse(a.timestamp || 0))
      .slice(0, MAX_EVENTS);
    const fragment = document.createDocumentFragment();
    if (!merged.length) {
      const row = makeElement("tr");
      const empty = makeElement("td", "", "No events observed by either local gateway or tunnel-client yet.");
      empty.colSpan = 5;
      row.append(empty);
      fragment.append(row);
    }
    for (const item of merged) {
      const row = makeElement("tr");
      row.append(makeElement("td", "mono", timeString(item.timestamp)));
      row.append(makeElement("td", "", item.client));
      row.append(makeElement("td", "", item.operation));
      row.append(makeElement("td", "mono", item.target));
      const cell = makeElement("td");
      cell.append(makeElement("span", "result-pill success", "Observed"));
      row.append(cell);
      fragment.append(row);
    }
    $("#activity-body").replaceChildren(fragment);
  }

  function recordEvent(event) {
    state.events.unshift(event);
    if (state.events.length > MAX_EVENTS) state.events.length = MAX_EVENTS;
    renderActivity();
  }

  async function refreshTunnelActivity() {
    try {
      const data = await api("/api/tunnel-activity");
      state.tunnelEvents = data?.available && Array.isArray(data.events) ? data.events : [];
    } catch {
      state.tunnelEvents = [];
    }
    renderActivity();
    if (state.selectedPid === null) renderSessions();
  }

  function startStream() {
    if (!("EventSource" in window)) {
      $("#stream-label").textContent = "Stream unavailable";
      return;
    }
    const stream = new EventSource("/api/stream");
    state.stream = stream;
    stream.onopen = () => {
      state.streamOnline = true;
      $("#stream-label").textContent = state.status?.workerMonitorConnected ? "Live · MCP worker observed" : "Live stream · MCP worker not observed";
      $("#stream-indicator").classList.add("connected");
    };
    stream.onerror = () => {
      state.streamOnline = false;
      $("#stream-label").textContent = "Reconnecting…";
      $("#stream-indicator").classList.remove("connected");
    };
    stream.addEventListener("output", (message) => {
      let event;
      try { event = JSON.parse(message.data); } catch { return; }
      if (!Number.isSafeInteger(event.pid) || event.pid <= 0 || typeof event.text !== "string") return;
      const current = state.lines.get(event.pid) || [];
      current.push({ timestamp: event.timestamp, stream: event.stream, text: event.text });
      state.lines.set(event.pid, current.slice(-MAX_LINES));
      recordEvent(event);
      if (state.followLatest && event.pid !== state.selectedPid) {
        // Let the sorted session list choose the newest process; older
        // background output must not steal focus from a newer session.
        void refreshSessions();
      } else if (event.pid === state.selectedPid) renderSessions();
      else if (!state.sessions.some((session) => session.pid === event.pid)) void refreshSessions();
    });
  }

  async function performConnection(action, confirmedExternal = false) {
    if (state.pendingConnection || !state.status) return;
    state.pendingConnection = true;
    state.pendingAction = action;
    renderConnection();
    try {
      const endpoint = action === "disconnect" && confirmedExternal ? "/api/disconnect?killAll=true" : `/api/${action}`;
      const response = await api(endpoint, { method: "POST" });
      if (response.status === "CONFIGURATION_REQUIRED") {
        if (response.localBridgeAvailable && window.confirm("OpenAI API key is not configured.\n\nWould you like to connect immediately via Zero-Config Local Bridge?\n(No OpenAI API key required)")) {
          void performConnection("connect?mode=local");
          return;
        }
        showToast("Open Settings → AI Connection to configure your key or use Local Bridge.");
        showView("settings");
        await refreshCredentialStatus();
      } else {
        showToast(`Tunnel request returned: ${response.status || "accepted"}. Checking actual state…`);
      }
    } catch (error) {
      showToast(`Cannot ${action}: ${error.message}`);
    } finally {
      state.pendingConnection = false;
      state.pendingAction = null;
      await refreshStatus();
    }
  }

  function onTunnelButton(slotNum) {
    if (!state.status) return;
    const tData = state.status.tunnels?.[String(slotNum)] || {};
    const connected = Boolean(tData.ready) || String(tData.state).toLowerCase() === "connected";
    if (connected) {
      const external = tData.owner === "external";
      state.disconnectTargetTunnel = slotNum;
      if (external || $("#setting-confirm").checked) {
        $("#disconnect-title").textContent = `Disconnect Tunnel ${slotNum}?`;
        $("#disconnect-description").textContent = external
          ? `Tunnel ${slotNum} (port ${tData.port || (slotNum === 1 ? 8080 : 8081)}) was started outside A2M. Confirming will stop the verified local daemon and disconnect its AI sessions.`
          : `This ends Tunnel ${slotNum} started by A2M. Active terminal processes may continue.`;
        if ($("#disconnect-dialog").showModal) $("#disconnect-dialog").showModal();
        else if (window.confirm($("#disconnect-description").textContent)) void performTunnelConnection(slotNum, "disconnect", external);
      } else {
        void performTunnelConnection(slotNum, "disconnect", false);
      }
    } else {
      void performTunnelConnection(slotNum, "connect", false);
    }
  }

  async function performTunnelConnection(slotNum, action, confirmedExternal = false) {
    if (!state.status) return;
    if (!state.pendingTunnelActions) state.pendingTunnelActions = {};
    if (state.pendingTunnelActions[slotNum]) return;

    state.pendingTunnelActions[slotNum] = action;
    renderConnection();
    try {
      let endpoint = `/api/${action}?tunnel=${slotNum}`;
      if (confirmedExternal && action === "disconnect") {
        endpoint += "&killAll=true";
      }
      const response = await api(endpoint, { method: "POST" });
      if (response.status === "CONFIGURATION_REQUIRED") {
        if (slotNum === 1 && response.localBridgeAvailable && window.confirm("OpenAI API key is not configured.\n\nWould you like to connect immediately via Zero-Config Local Bridge?\n(No OpenAI API key required)")) {
          void performTunnelConnection(1, "connect?mode=local", false);
          return;
        }
        showToast(response.message || `Tunnel ${slotNum} key not configured.`);
        showView("settings");
      } else {
        showToast(`Tunnel ${slotNum} ${action}: ${response.status || "accepted"}. Checking state…`);
      }
    } catch (error) {
      showToast(`Tunnel ${slotNum} ${action} error: ${error.message}`);
    } finally {
      delete state.pendingTunnelActions[slotNum];
      await refreshStatus();
    }
  }

  async function stopSelectedProcess() {
    const process = state.sessions.find((s) => s.pid === state.selectedPid);
    if (!sessionIsRunning(process)) return;
    if (!window.confirm(`Stop tracked process PID ${process.pid}?\n\n${process.command}\n\nThis will terminate the process group.`)) return;
    const button = $("#stop-session");
    button.disabled = true;
    try {
      await api(`/api/kill?pid=${encodeURIComponent(process.pid)}&source=${encodeURIComponent(process.source || "dashboard")}`, { method: "POST" });
      showToast(`Stop requested for process ${process.pid}.`);
    } catch (error) {
      showToast(`Could not stop process: ${error.message}`);
    } finally {
      await refreshSessions();
    }
  }

  async function copyOutput() {
    const lines = state.lines.get(state.selectedPid) || [];
    const text = lines.map((line) => `[${timeString(line.timestamp)}] ${line.stream}: ${line.text}`).join("\n");
    if (!text) return showToast("No process output to copy.");
    try {
      if (!navigator.clipboard?.writeText) throw new Error("Clipboard API unavailable");
      await navigator.clipboard.writeText(text);
      showToast("Process output copied.");
    } catch {
      const area = makeElement("textarea");
      area.value = text;
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.append(area);
      area.select();
      const copied = document.execCommand("copy");
      area.remove();
      showToast(copied ? "Process output copied." : "Clipboard unavailable.");
    }
  }

  async function exportDiagnostics() {
    try {
      const data = await api("/api/diagnostics");
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const link = makeElement("a");
      link.href = url;
      link.download = "a2m-gateway-diagnostics.json";
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      showToast("Gateway diagnostics exported.");
    } catch (error) {
      showToast(`Diagnostics unavailable: ${error.message}`);
    }
  }

  // ── Credential management ─────────────────────────────────────────────────
  async function refreshCredentialStatus() {
    try {
      const data = await api("/api/credentials/tunnel");
      const panel = $("#credential-setup-panel");
      const badge = $("#credential-status-badge");
      if (!panel || !badge) return;
      if (data.configured) {
        panel.hidden = true;
        badge.textContent = `Configured (source: ${data.source})`;
        badge.className = "cred-badge cred-ok";
      } else {
        panel.hidden = false;
        badge.textContent = "Not configured";
        badge.className = "cred-badge cred-warn";
      }
    } catch (_) {
      // Silently ignore if endpoint is unreachable (gateway not yet running)
    }
  }

  async function saveCredential() {
    const input = $("#cred-api-key-input");
    const key = (input?.value || "").trim();
    if (!key) { showToast("Please enter a valid API key."); return; }
    try {
      const result = await api("/api/credentials/tunnel", { method: "POST", body: JSON.stringify({ api_key: key }) });
      if (result.status === "saved") {
        showToast("API key saved to macOS Keychain ✓");
        input.value = "";
        await refreshCredentialStatus();
      }
    } catch (err) {
      showToast(`Failed to save key: ${err.message}`);
    }
  }

  async function deleteCredential() {
    if (!window.confirm("Remove the stored API key from macOS Keychain?")) return;
    try {
      await api("/api/credentials/tunnel", { method: "DELETE" });
      showToast("API key removed from Keychain.");
      await refreshCredentialStatus();
    } catch (err) {
      showToast(`Failed to remove key: ${err.message}`);
    }
  }

  function initialize() {
    const embedded = new URLSearchParams(location.search).get("embedded") === "1";
    if (embedded) document.body.classList.add("is-embedded");
    document.querySelectorAll("[data-nav]").forEach((button) =>
      button.addEventListener("click", () => showView(button.dataset.nav)));
    document.querySelectorAll("[data-go]").forEach((button) =>
      button.addEventListener("click", () => showView(button.dataset.go)));
    document.querySelectorAll("[data-copy]").forEach((button) =>
      button.addEventListener("click", () => { void copyOutput(); }));
    document.querySelectorAll("[data-action]").forEach((button) => {
      if (button.dataset.action === "export-diagnostics") {
        button.addEventListener("click", () => { void exportDiagnostics(); });
      } else if (button.dataset.action === "stop-session") {
        button.addEventListener("click", () => { void stopSelectedProcess(); });
      }
    });
    $("#tunnel-1-button")?.addEventListener("click", () => onTunnelButton(1));
    $("#tunnel-2-button")?.addEventListener("click", () => onTunnelButton(2));
    $("#capability-refresh").addEventListener("click", () => { void refreshCapabilities(); });
    // Credential setup buttons (present only when credential-setup-panel is in DOM)
    document.getElementById("cred-save-btn")?.addEventListener("click", () => { void saveCredential(); });
    document.getElementById("cred-delete-btn")?.addEventListener("click", () => { void deleteCredential(); });
    document.getElementById("start-local-bridge-btn")?.addEventListener("click", () => {
      showToast("Starting Zero-Config Local In-Memory Bridge…");
      void performTunnelConnection(1, "connect?mode=local", false);
      showView("overview");
    });
    $("#follow-latest-button").addEventListener("click", () => {
      state.followLatest = !state.followLatest;
      if (state.followLatest) void refreshSessions();
      else renderSessions();
    });
    $("#cancel-disconnect").addEventListener("click", () => $("#disconnect-dialog").close());
    $("#confirm-disconnect").addEventListener("click", () => {
      $("#disconnect-dialog").close();
      const target = state.disconnectTargetTunnel || 1;
      const tData = state.status?.tunnels?.[String(target)] || {};
      void performTunnelConnection(target, "disconnect", tData.owner === "external");
    });
    $("#disconnect-dialog").addEventListener("click", (event) => {
      if (event.target === $("#disconnect-dialog")) $("#disconnect-dialog").close();
    });
    $("#setting-terminal-preview").addEventListener("change", (event) => {
      $("#view-overview .operations-panel").hidden = !event.target.checked;
    });
    $("#setting-autoscroll").addEventListener("change", (event) => {
      $("#overview-autoscroll").checked = event.target.checked;
      $("#full-autoscroll").checked = event.target.checked;
    });
    $("#overview-autoscroll").addEventListener("change", (event) => {
      $("#full-autoscroll").checked = event.target.checked;
      $("#setting-autoscroll").checked = event.target.checked;
    });
    $("#full-autoscroll").addEventListener("change", (event) => {
      $("#overview-autoscroll").checked = event.target.checked;
      $("#setting-autoscroll").checked = event.target.checked;
    });
    document.addEventListener("keydown", (event) => {
      if ((event.metaKey || event.ctrlKey) && /^[1-5]$/.test(event.key)) {
        event.preventDefault();
        showView(VIEWS[Number(event.key) - 1]);
      }
    });
    renderConnection();
    renderSessions();
    if (location.protocol === "file:") {
      $("#app-mode").textContent = "STATIC FILE";
      $("#api-connection").textContent = "Open via A2M.app";
      showToast("Open this UI through the local Go Gateway or A2M.app for live data.");
      return;
    }
    void refreshStatus();
    void refreshTunnelActivity();
    void refreshSessions().then(startStream);
    void refreshCredentialStatus();
    setInterval(() => {
      void refreshStatus();
      void refreshSessions();
      void refreshTunnelActivity();
    }, 4000);
    addEventListener("beforeunload", () => state.stream?.close(), { once: true });
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", initialize, { once: true });
  else initialize();
})();

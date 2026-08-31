  const PV = 1, LINE = 1.25;
  // Terminal type size. ⌘+/⌘-/⌘0 rescale it (see setFontSize); it is a per-
  // browser display preference, so it lives in localStorage rather than in the
  // session the server persists.
  const FONT_DEFAULT = 14, FONT_MIN = 9, FONT_MAX = 32, FONT_KEY = "cats.font_px";
  let FONT_PX = FONT_DEFAULT;
  try {
    const stored = parseInt(localStorage.getItem(FONT_KEY), 10);
    if (stored >= FONT_MIN && stored <= FONT_MAX) FONT_PX = stored;
  } catch (e) { /* storage disabled (private mode / file://) — keep the default */ }

  const panesEl = document.getElementById("panes");
  const wsListEl = document.getElementById("ws-list");
  const wsGlobalTodoEl = document.getElementById("ws-global-todo");
  const wsCountEl = document.getElementById("ws-count");
  const paneListEl = document.getElementById("pane-list");
  const agentListEl = document.getElementById("agent-list");
  const usageListEl = document.getElementById("usage-list");
  const hostSecEl = document.getElementById("sec-hosts");
  const hostListEl = document.getElementById("host-list");
  const rbSecEl = document.getElementById("sec-runbooks");
  const rbListEl = document.getElementById("rb-list");
  const histSecEl = document.getElementById("sec-history");
  const histListEl = document.getElementById("hist-list");
  const tabbarEl = document.getElementById("tabbar");
  const toastsEl = document.getElementById("toasts");
  const palHintEl = document.getElementById("palhint");
  const pluginsBtnEl = document.getElementById("pluginsbtn");
  const recBtnEl = document.getElementById("recbtn");
  const gearEl = document.getElementById("gear");
  const bannerEl = document.getElementById("banner");
  const splitterEl = document.getElementById("splitter");
  const chatEl = document.getElementById("chat");
  const chatLogEl = document.getElementById("chat-log");
  const chatTitleEl = document.getElementById("chat-title");
  const chatStatusEl = document.getElementById("chat-status");
  const chatInputEl = document.getElementById("chat-input");
  const chatBtnEl = document.getElementById("chatbtn");
  const chatStopEl = document.getElementById("chat-stop");
  const chatClearEl = document.getElementById("chat-clear");
  const dpr = window.devicePixelRatio || 1;
  const measureCtx = document.createElement("canvas").getContext("2d");

  // Canvas colors, read from the theme's CSS custom properties so they follow
  // a theme switch (readThemeVars re-reads them; the literals here are the
  // last-resort fallbacks matching the default theme).
  // THEME_FG/BG: defaults used when a packed cell color is 0 (frame defaults
  // unknown). SEL_FILL: browser-local drag-selection wash (non-mouse panes,
  // §7 read). CM_CURSOR: keyboard copy-mode cursor outline, distinct from the
  // wash. SCROLL_THUMB(_IDLE): the scrollback scrollbar's two thumb states.
  let THEME_FG = 0xd6ddd6, THEME_BG = 0x1f2420;
  let SEL_FILL = "rgba(88,204,140,0.30)";
  let CM_CURSOR = "rgba(122,220,170,0.95)";
  let SCROLL_THUMB = "rgba(122,220,170,0.6)", SCROLL_THUMB_IDLE = "rgba(255,255,255,0.25)";

  // readThemeVars pulls the canvas-side colors out of the effective CSS custom
  // properties. Called at startup and whenever a theme lands (the injected
  // page style, a live "theme" push, the settings modal's preview).
  function readThemeVars() {
    const cs = getComputedStyle(document.documentElement);
    const v = (name) => cs.getPropertyValue(name).trim();
    const packed = (name) => {
      const m = /^#([0-9a-fA-F]{6})$/.exec(v(name));
      return m ? parseInt(m[1], 16) : null;
    };
    THEME_FG = packed("--term-fg") ?? THEME_FG;
    THEME_BG = packed("--term-bg") ?? THEME_BG;
    SEL_FILL = v("--sel-fill") || SEL_FILL;
    CM_CURSOR = v("--cm-cursor") || CM_CURSOR;
    SCROLL_THUMB = v("--scroll-thumb") || SCROLL_THUMB;
    SCROLL_THUMB_IDLE = v("--scroll-thumb-idle") || SCROLL_THUMB_IDLE;
  }
  readThemeVars();

  // applyThemeInline paints a full resolved palette (and optionally a font)
  // as inline :root custom properties — the live-theme path shared by the
  // server's "theme" push and the settings modal's preview. Inline properties
  // sit above the page's baked <style> in the cascade, so the last theme
  // applied always wins, and the canvases re-read their colors and repaint.
  function applyThemeInline(colors, font) {
    const root = document.documentElement;
    for (const k in colors) root.style.setProperty("--" + k, colors[k]);
    if (font) { root.style.fontFamily = font; document.body.style.fontFamily = font; }
    readThemeVars();
    for (const p of panes.values()) scheduleDraw(p);
  }

  // ratatui Modifier bits (cell.m).
  const M_BOLD = 0x1, M_DIM = 0x2, M_ITALIC = 0x4, M_UNDERLINED = 0x8, M_REVERSED = 0x40, M_HIDDEN = 0x80;

  let ws = null, cellW = 9, cellH = 19, cols = 0, rows = 0;
  let layoutMsg = null;          // last layout message
  let tabZoomed = false;         // active tab zoomed (applyLayout; header ZOOM chip)
  const panes = new Map();       // pane id -> pane state
  let winFocused = document.hasFocus(); // window in OS foreground (see onWinFocus)
  let copyModePane = null;       // the pane in keyboard copy-mode, or null
  let agentItems = [];           // last agents rollup (drives sidebar + workspace summaries)
  // Last pane.list snapshot: every pane in the session, across all workspaces and
  // tabs (the Panes sidebar section, see renderPaneList). The layout message can't
  // serve it — it carries the active tab's panes alone — so this is a query result
  // held between refreshes. busy/again/wait coalesce refresh requests so a chatty
  // pane can't queue one round trip per title change (refreshPaneList).
  let paneInv = [], paneInvBusy = false, paneInvAgain = false, paneInvWait = null;
  const SVGNS = "http://www.w3.org/2000/svg"; // createElementNS: SVG isn't HTML
  // Workspace ids whose pane group is folded shut in the sidebar, and the ids the
  // last render actually drew (what collapse-all/expand-all act on). Which groups
  // you keep open is a per-browser display preference like the font size, so it
  // persists in localStorage rather than in the session the server owns.
  const PGRP_KEY = "cats.panes.collapsed";
  let paneCollapsed = new Set(), paneGroupIDs = [];
  try {
    const raw = JSON.parse(localStorage.getItem(PGRP_KEY));
    if (Array.isArray(raw)) paneCollapsed = new Set(raw);
  } catch (e) { /* storage disabled or corrupt — start with everything expanded */ }
  function savePaneCollapsed() {
    try { localStorage.setItem(PGRP_KEY, JSON.stringify([...paneCollapsed])); } catch (e) { /* not persisted */ }
  }
  // Is the "more workspaces…" shelf at the foot of the Panes section open? That
  // shelf holds the workspaces with nothing running in them (renderPaneList), and
  // it is a flag rather than another id in the set above for one reason: it
  // defaults the other way. A set of collapsed ids says "everything is open until
  // you fold it", which is right for a group you chose to fold and wrong here —
  // the whole point of the shelf is that a session's idle workspaces are out of
  // the way before anyone touches anything. Stored as its own key so that default
  // survives a browser that has never seen the section.
  const PMORE_KEY = "cats.panes.moreopen";
  let paneMoreOpen = false;
  try { paneMoreOpen = localStorage.getItem(PMORE_KEY) === "1"; } catch (e) { /* storage disabled — shelf starts shut */ }
  function savePaneMoreOpen() {
    try { localStorage.setItem(PMORE_KEY, paneMoreOpen ? "1" : "0"); } catch (e) { /* not persisted */ }
  }
  // The same arrangement for the Usage section's provider groups, kept separate
  // from the pane groups rather than merged into one map: the two sections'
  // group ids come from different namespaces (workspace ids vs. provider ids),
  // and folding CLAUDE has nothing to do with folding a workspace of panes.
  const UGRP_KEY = "cats.usage.collapsed";
  let usageCollapsed = new Set(), usageGroupIDs = [];
  try {
    const raw = JSON.parse(localStorage.getItem(UGRP_KEY));
    if (Array.isArray(raw)) usageCollapsed = new Set(raw);
  } catch (e) { /* storage disabled or corrupt — start with everything expanded */ }
  function saveUsageCollapsed() {
    try { localStorage.setItem(UGRP_KEY, JSON.stringify([...usageCollapsed])); } catch (e) { /* not persisted */ }
  }
  // And once more for the Workspaces section, whose groups are the two states a
  // workspace can be in rather than a list of ids: WS_OPEN and WS_LOCKED. Its own
  // key for the same reason as above — the namespaces don't overlap, and a folded
  // "locked" shelf says nothing about which provider or which workspace of panes
  // you last folded.
  const WGRP_KEY = "cats.workspaces.collapsed";
  const WS_OPEN = "open", WS_LOCKED = "locked";
  // The third id is not a shelf. With nothing locked there is only one shelf and
  // no header is drawn for it (renderWorkspaces), so the fold pair would have
  // nothing to act on — WS_ALL is what it acts on instead: the section's own list,
  // folded behind the count that then appears on the Workspaces heading. It shares
  // the set above because it is the same question ("what is folded in this
  // section?") and because collapse-all rewrites the whole set from the ids the
  // last render drew, which keeps the two spellings from ever both being present.
  const WS_ALL = "all";
  let wsCollapsed = new Set(), wsGroupIDs = [];
  try {
    const raw = JSON.parse(localStorage.getItem(WGRP_KEY));
    if (Array.isArray(raw)) wsCollapsed = new Set(raw);
  } catch (e) { /* storage disabled or corrupt — start with everything expanded */ }
  function saveWsCollapsed() {
    try { localStorage.setItem(WGRP_KEY, JSON.stringify([...wsCollapsed])); } catch (e) { /* not persisted */ }
  }
  // And, one tier up from all three: which whole sidebar *sections* are folded
  // shut, by section element id. This one set covers all four sections rather
  // than splitting per section like the group sets above, because here the ids
  // do share a namespace — they are the sections themselves — and nothing ever
  // folds them in bulk, so there is no collapse-all to keep the spellings apart.
  // Same localStorage reasoning as the rest: which parts of the sidebar you keep
  // open is a per-browser display preference, not session state the server owns.
  const SECT_KEY = "cats.sections.collapsed";
  let sectCollapsed = new Set();
  try {
    const raw = JSON.parse(localStorage.getItem(SECT_KEY));
    if (Array.isArray(raw)) sectCollapsed = new Set(raw);
  } catch (e) { /* storage disabled or corrupt — start with every section open */ }
  function saveSectCollapsed() {
    try { localStorage.setItem(SECT_KEY, JSON.stringify([...sectCollapsed])); } catch (e) { /* not persisted */ }
  }

  function measure() {
    measureCtx.font = `${FONT_PX}px ui-monospace, Menlo, monospace`;
    cellW = Math.max(1, Math.round(measureCtx.measureText("M").width * 100) / 100);
    cellH = Math.ceil(FONT_PX * LINE);
  }

  // The grid is the size of the #panes container (chrome is real HTML around
  // it), so the server's layout rects map straight onto it.
  function gridSize() {
    const w = panesEl.clientWidth || window.innerWidth;
    const h = panesEl.clientHeight || window.innerHeight;
    cols = Math.max(20, Math.floor(w / cellW));
    rows = Math.max(6, Math.floor(h / cellH));
  }

  // setFontSize rescales the terminal type. cellW/cellH are read live by both
  // the layout math and draw(), so re-measuring, re-deriving the grid, and
  // telling the server the new cols/rows is the whole job: the last layout is
  // re-applied against the new cell size to keep the frame correct until the
  // server's relayout lands (a bigger font means fewer cells, so its rects
  // change too). Cell pixel metrics reported at init aren't resent — β only
  // consumes them when a pane is created.
  function setFontSize(px) {
    px = Math.min(FONT_MAX, Math.max(FONT_MIN, Math.round(px)));
    if (px === FONT_PX) return;
    FONT_PX = px;
    try { localStorage.setItem(FONT_KEY, String(px)); } catch (e) { /* not persisted */ }
    measure();
    gridSize();
    sendMsg({ t: "resize", cols, rows });
    if (layoutMsg) applyLayout(layoutMsg);
    toast(`font ${px}px`);
  }

  // Hook for the mac app's View menu. In a WKWebView, Cocoa resolves ⌘+/⌘-/⌘0
  // as menu key equivalents before the page's keydown handler runs, so the
  // native menu items call back into Go, which Evals this. delta of 0 resets.
  window.catsAdjustFont = (delta) => setFontSize(delta ? FONT_PX + delta : FONT_DEFAULT);


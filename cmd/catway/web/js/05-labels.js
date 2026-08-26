  // modelLabel condenses a pane's resolved model (agentmodel.go builds it —
  // "claude-opus-5 · high", "claude-sonnet-4-5-20250929[1m]") down to the family
  // word and its version, which is what the pane header and the sidebar rows
  // name: "opus 5", "sonnet 4.5 [1M]", "gpt 5.4 mini".
  //
  // The version rides along because a family word alone stops distinguishing
  // rows the moment two generations of it are in play — "opus" beside "opus"
  // says nothing about which pane is on the newer one, and a model switch that
  // only moved the generation would leave the label unchanged.
  //
  // The 1M-context marker survives the trim because it changes how much the
  // pane can hold; the effort suffix does not, since the hover card still shows
  // the whole string untouched. The family is the first alphabetic token that
  // isn't the "claude" vendor prefix, which finds it in both id orderings
  // ("claude-opus-4-8" and the older "claude-3-5-sonnet-20241022"). "" when
  // nothing qualifies, so callers fall back to the agent's own name rather than
  // printing a mangled id.
  //
  // A size qualifier is kept alongside the family ("gpt-5.4-mini" -> "gpt 5.4
  // mini") because for the OpenAI-shaped ids a copilot pane reports it is the
  // whole distinction: the family alone collapses gpt-5.4 and gpt-5.4-mini onto
  // the same word, in a list whose entire job is telling rows apart. Anthropic
  // ids are unaffected — their family token already carries the tier.
  const MODEL_SIZES = ["mini", "nano", "micro", "flash", "lite"];
  // A version token is one or two digits per component ("5", "4", "5.4"). The
  // bound is what keeps the trailing release date out — "20250929" is a token of
  // the same shape and sits exactly where a version component would.
  const MODEL_VER = /^\d{1,3}(\.\d{1,3})*$/;
  // modelVersion reads the run of version tokens adjacent to the family at
  // toks[i], joined with dots: "claude-opus-5" -> "5", "claude-sonnet-4-5-…" ->
  // "4.5". Forward first, since every current id puts the version after the
  // family; the backward walk covers the older ordering, where the digits lead
  // ("claude-3-5-sonnet-20241022" -> "3.5"). Both walks stop at the first token
  // that isn't a version, so the date and the "claude" prefix bound them.
  function modelVersion(toks, i) {
    const fwd = [];
    for (let j = i + 1; j < toks.length && MODEL_VER.test(toks[j]); j++) fwd.push(toks[j]);
    if (fwd.length) return fwd.join(".");
    const back = [];
    for (let j = i - 1; j >= 0 && MODEL_VER.test(toks[j]); j--) back.unshift(toks[j]);
    return back.join(".");
  }
  function modelLabel(model) {
    if (!model) return "";
    let id = model.split("·")[0].trim().toLowerCase();
    let wide = "";
    if (id.endsWith("[1m]")) { wide = " [1M]"; id = id.slice(0, -4); }
    const toks = id.split("-");
    for (let i = 0; i < toks.length; i++) {
      const tok = toks[i];
      if (tok === "claude" || !/^[a-z]+$/.test(tok)) continue;
      // Look past the family for a qualifier, but never back at it: the scan
      // starts after the family so a family that is itself in the list (were one
      // ever added) cannot be appended to itself.
      const size = toks.slice(i + 1).find((t) => MODEL_SIZES.includes(t));
      const ver = modelVersion(toks, i);
      return tok + (ver ? " " + ver : "") + (size ? " " + size : "") + wide;
    }
    return "";
  }

  // agentLabel is what every place that names a pane's agent prints: the agent's
  // own name, then the model it is running — "claude opus 5", "copilot gpt 5.4
  // mini", "codex" for an agent whose model can't be read.
  //
  // The agent name leads even though it repeats down a list of claude panes,
  // because the lists are no longer single-agent: with copilot and claude panes
  // interleaved, the model word alone leaves "sonnet 4.5" ambiguous between a
  // claude pane and a copilot pane running an Anthropic model through its own
  // harness — a distinction that decides which tool you are about to talk to.
  //
  // Either half may be missing: no model until the agent's first answer (or for
  // an agent with no resolver), and no agent on a plain shell pane, which is why
  // callers can hand this both and let it print whichever it has.
  function agentLabel(agent, model) {
    const m = modelLabel(model);
    if (!agent) return m;
    return m ? agent + " " + m : agent;
  }

  // AGENT_HUE assigns each known agent one of the six --agent-N identity slots
  // (see the :root block). Six slots, ~18 known labels, so the map is a seating
  // chart rather than a lookup: the first six entries are the agents most likely
  // to share a sidebar, and they take the six slots outright. Everything after
  // wraps, so a collision only happens between agents further down the list.
  //
  // The labels are detect.IdentifyAgent's canonical vocabulary
  // (internal/detect/detect.go) plus ced and dbc, which reach the rollup
  // through the hook path as custom sources rather than through binary
  // detection. That is the normal case, not the exception: any tool can report
  // any label it likes, which is why the fallback below matters more than this
  // table does.
  const AGENT_HUE = {
    claude: 1, ced: 2, codex: 3, copilot: 4, cursor: 5, gemini: 6,
    droid: 1, amp: 2, pi: 3, kimi: 4, opencode: 5, hermes: 6,
    kilo: 1, kiro: 2, agy: 3, cline: 4, grok: 5, qodercli: 6,
    // The house tools are seated in the order they arrived — claude, ced,
    // dbc, gonotes — because they are the ones that actually share this
    // sidebar. dbc is listed rather than left to the hash for that reason
    // alone: the hash would have put it on 5, beside cursor, and the point of
    // the seating chart is to choose the neighbours rather than accept them.
    dbc: 3,
    // gonotes had the worse draw: the hash puts it on 1, which is claude's —
    // and a note-taking pane sits beside a claude pane more often than beside
    // anything else, since capturing that pane's output is what it is for.
    gonotes: 4,
  };

  // hueClass answers for labels the map has never seen — the ced case before
  // anyone edits AGENT_HUE, and every tool that ships after this build. Hashing
  // the label is what makes an unknown agent *consistent* rather than merely
  // coloured: the same tool lands on the same hue in every row, every render and
  // every restart, which is the whole property the colour is being read for. The
  // alternative (falling back to plain --fg-strong) would have left exactly the
  // agents the user is still learning to recognise as the undifferentiated ones.
  //
  // FNV-1a, 32-bit, kept in integer range by Math.imul — any stable hash does,
  // and this one is short enough to read.
  function hueClass(agent) {
    if (!agent) return "";
    const slot = AGENT_HUE[agent];
    if (slot) return "ah" + slot;
    let h = 0x811c9dc5;
    for (let i = 0; i < agent.length; i++) {
      h = Math.imul(h ^ agent.charCodeAt(i), 0x01000193);
    }
    return "ah" + ((h >>> 0) % 6 + 1);
  }

  // HOME is the server's home directory, baked into the page at render time
  // (page.go's homeScript). "" — no home, or an old page — simply turns the
  // abbreviation off and paths draw in full.
  const HOME = (typeof window.__catsHome === "string" ? window.__catsHome : "").replace(/\/+$/, "");
  // shortPath renders an absolute path the way a shell prompt does. Display
  // only: p.cwd keeps the absolute path it arrived as, because that same value
  // is handed back to the server as a new tab's spawn directory (see
  // focusedPaneCwd), and "~" is not a directory anything can spawn in.
  function shortPath(path) {
    if (!path || !HOME) return path;
    if (path === HOME) return "~";
    if (path.startsWith(HOME + "/")) return "~" + path.slice(HOME.length);
    return path;
  }


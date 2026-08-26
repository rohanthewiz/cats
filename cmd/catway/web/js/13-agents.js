  // ---- Agents rollup (WS8): the `agents` message, previously dropped ----

  // Coarse age of a state, one unit deep — the row is a glance, not a stopwatch.
  // Split in two because the agents rows emphasize the figure without the word:
  // fmtAgeNum is the reading, fmtAge the whole phrase everything else wants.
  function fmtAgeNum(ms) {
    const s = Math.max(0, Math.round(ms / 1000));
    if (s < 60) return s + "s";
    const m = Math.round(s / 60);
    if (m < 60) return m + "m";
    const h = Math.round(m / 60);
    return h < 24 ? h + "h" : Math.round(h / 24) + "d";
  }
  function fmtAge(ms) { return fmtAgeNum(ms) + " ago"; }

  // An agent's prompt cache expires roughly an hour after its last turn, and a
  // state that hasn't moved is a proxy for a turn that hasn't happened. 45
  // minutes is the point where saying so is still actionable: enough of the hour
  // left to go answer the agent, rather than a post-mortem.
  const CACHE_WARN_MIN = 45;

  // paintAge (re)writes one row's age label, in the two pieces the warning needs
  // — the figure, then " ago · " leading into the state text.
  //
  // Only the two *settled* states can warn. Idle and done-unseen are both an
  // agent that stopped and is waiting on the user, so their age is dead time and
  // measures the cache draining. A working agent's identical-looking age is
  // run-time — it is spending the cache, not losing it — and a blocked one is
  // mid-turn as well, so colouring either would say the opposite of the truth.
  //
  // Measured against the *rounded* reading, not the raw age: the label rounds to
  // whole minutes, so a raw-ms threshold would leave a plain "45m" on screen for
  // up to half a minute before it bolted — two rows both reading 45m, one bold,
  // looks like a bug. Any hour- or day-scale reading clears 45 minutes trivially.
  function paintAge(el, ms, st) {
    const warn = Math.round(ms / 60000) >= CACHE_WARN_MIN && (st === "idle" || st === "done");
    el.className = "aage" + (warn ? " stale stale-" + st : "");
    const num = document.createElement("span");
    num.className = "anum";
    num.textContent = fmtAgeNum(ms);
    el.textContent = "";
    el.appendChild(num);
    el.appendChild(document.createTextNode(" ago · "));
  }

  // The rollup only arrives when a state moves, so the ages it stamped would sit
  // frozen in between. One shared tick re-reads each row's absolute instant —
  // every 5s, not every second: the label is a glance at staleness, and it is
  // cheaper to let it lag a few seconds than to touch every row that often.
  //
  // This tick is also the only thing that can raise the 45-minute warning: a row
  // crosses that line precisely because nothing happened, so there is no rollup
  // coming to redraw it. Hence the state travels on the element (data-st) rather
  // than being closed over at render time.
  setInterval(() => {
    for (const el of agentListEl.querySelectorAll(".aage")) {
      paintAge(el, Date.now() - Number(el.dataset.at), el.dataset.st);
    }
  }, 5000);

  // ---- Auto-reveal (pairs with setSidebarHidden) ----
  // Folding the column away is meant as a temporary "get out of my way", not as
  // a way to miss the two agent states that are actually addressed to the user:
  // blocked (it is waiting on an answer and nothing moves until it gets one) and
  // done-unseen (it finished while the eye was elsewhere). Either one arriving
  // brings the sidebar back on its own.
  //
  // Edge-triggered against the previous rollup, per pane: an agent that is
  // merely *still* blocked must not re-open a column the user has deliberately
  // folded away since, so only the transition into the state counts, never the
  // state itself. agentAttn starts null and the first rollup seeds it without
  // firing — otherwise every already-finished-unseen agent would read as a fresh
  // transition on page load and a hidden sidebar could never survive a reload.
  // The map outlives a dropped socket, so a reconnect's rollup doesn't re-fire
  // for states the user has already seen.
  let agentAttn = null;
  function agentAttentionSweep(items) {
    const next = new Map();
    let pop = null;
    for (const it of items) {
      const st = markerState(it);
      next.set(it.pane, st);
      if (!pop && agentAttn && (st === "blocked" || st === "done") && agentAttn.get(it.pane) !== st) {
        pop = { it, st };  // first one wins; the list is already in the server's order
      }
    }
    agentAttn = next;
    if (!pop) { if (sidebarHidden()) paintSplitterAttention(); return; }
    if (!sidebarHidden()) return;
    setSidebarHidden(false);
    // A column that reappears by itself is a surprise unless it says why, and
    // the row that caused it is about to be one of several in AGENTS.
    toast(agentLabel(pop.it.agent, pop.it.model) +
      (pop.st === "blocked" ? " is blocked" : " finished") + " — sidebar reopened");
  }


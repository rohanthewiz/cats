  // ---- Usage: what the agents on this machine have spent, and against what ----

  // The server polls every couple of minutes, so the reset countdowns it
  // stamped would sit frozen in between — same problem the agent-age labels
  // have, and the same fix: keep the absolute instant on the element and
  // re-render the label on a tick of our own.
  let usageMsg = null;

  // The section's heading controls: fold or unfold every group at once, how old
  // the reading is, and a way to take a new one. Built here rather than in the
  // markup so the age label lives beside the code that maintains it — and beside
  // the button whose whole purpose is to reset it.
  //
  // Order across the heading: the reading (how old), then the control that
  // renews it, then the fold pair hard against the right edge. The age and its
  // refresh belong together — the button's whole job is to reset the number
  // beside it — so the pair that has nothing to do with either is what moves
  // outboard rather than splitting them. That also puts the fold pair in the one
  // place it is the same distance from every section's edge, which is what lets
  // Usage, Workspaces and Panes each be folded without re-aiming the pointer.
  // Like Panes', these act on the groups the last draw emitted, so a provider
  // that stops reporting doesn't linger in the collapsed set.
  let usageAgeEl = null, usageBtnEl = null, usageBusyTimer = null;
  (function initUsageHeadingCtl() {
    const el = document.getElementById("usage-hctl");
    usageAgeEl = document.createElement("span");
    usageAgeEl.className = "hage";
    el.appendChild(usageAgeEl);
    usageBtnEl = mkBtn("", "read the account's usage now", "uref", refreshUsage);
    usageBtnEl.appendChild(refreshMark());
    el.appendChild(usageBtnEl);
    el.appendChild(mkBtn("⊞", "expand all usage groups", "", () => {
      usageCollapsed.clear(); saveUsageCollapsed(); drawUsage();
    }));
    el.appendChild(mkBtn("⊟", "collapse all usage groups", "", () => {
      usageCollapsed = new Set(usageGroupIDs); saveUsageCollapsed(); drawUsage();
    }));
    initSectionFold("sec-usage", "usage-hctl", "usage");
  })();

  // refreshMark: the circular arrow on the refresh control — an arc most of the
  // way round with a head on the loose end, the two strokes that survive at
  // 12px. Drawn rather than typed because the ⟳ glyph is rendered small for its
  // point size in a monospace face, which no font-size that also fits the
  // heading can fix.
  function refreshMark() {
    const svg = document.createElementNS(SVGNS, "svg");
    svg.setAttribute("class", "refresh");
    svg.setAttribute("viewBox", "0 0 16 16");
    svg.setAttribute("fill", "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "1.6");
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");
    for (const d of ["M13.7 10A6 6 0 1 1 12.3 3.8L15.3 6.7", // the arc, opening at the top right
                     "M15.3 2.7V6.7H11.3"]) {                 // the head it opens into
      const path = document.createElementNS(SVGNS, "path");
      path.setAttribute("d", d);
      svg.appendChild(path);
    }
    return svg;
  }

  // refreshUsage asks the server to poll now. The reading is NOT the command's
  // reply — the server acks the ask and broadcasts the numbers to every client
  // when the read lands — so the button stays busy until a usage message
  // arrives. Two backstops clear it otherwise: an explicit failure (a server
  // too old to know the command answers with one), and a timer, for a read that
  // hangs long enough that a spinning icon would be a lie.
  function refreshUsage() {
    if (usageBtnEl.classList.contains("busy")) return; // a read is already out
    usageBtnEl.classList.add("busy");
    clearTimeout(usageBusyTimer);
    usageBusyTimer = setTimeout(usageIdle, 20000);
    sendCmdAwait("usage.refresh", null, (res) => {
      if (res.ok) return;
      usageIdle();
      toast("usage: " + (res.error || "refresh failed"));
    });
  }

  function usageIdle() {
    clearTimeout(usageBusyTimer); usageBusyTimer = null;
    usageBtnEl.classList.remove("busy");
  }

  // The heading's age label: how old the numbers in the section are. It reads
  // the message's own stamp rather than when this browser received it, because
  // a page that connects mid-interval is handed the server's STORED reading —
  // fresh to arrive, minutes old to be true.
  function renderUsageAge() {
    const at = usageMsg && usageMsg.read_at ? Date.parse(usageMsg.read_at) : NaN;
    if (Number.isNaN(at)) { usageAgeEl.textContent = ""; usageAgeEl.title = ""; return; }
    usageAgeEl.textContent = fmtAge(Date.now() - at);
    usageAgeEl.title = "read at " + new Date(at).toLocaleTimeString();
  }

  // Coarse time-to-reset, one unit deep, in the same register as fmtAge.
  function fmtUntil(ms) {
    const s = Math.round(ms / 1000);
    if (s <= 0) return "resetting";
    if (s < 60) return "resets in " + s + "s";
    const m = Math.round(s / 60);
    if (m < 60) return "resets in " + m + "m";
    const h = Math.round(m / 60);
    return h < 24 ? "resets in " + h + "h" : "resets in " + Math.round(h / 24) + "d";
  }

  // The same countdown with no sentence around it, for the folded group's chip —
  // "3% (2hrs)" — where the percentage has already said what is being counted and
  // only the horizon is missing. Hours carry the word because hours are the unit
  // a 5-hour window is actually read in, and "2h" beside "3%" scans as another
  // measurement rather than as a clock; below the hour the terse register the
  // rest of the sidebar uses is unambiguous on its own.
  function fmtLeft(ms) {
    const s = Math.round(ms / 1000);
    if (s <= 0) return "resetting";
    if (s < 60) return s + "s";
    const m = Math.round(s / 60);
    if (m < 60) return m + "m";
    const h = Math.round(m / 60);
    if (h < 24) return h + (h === 1 ? "hr" : "hrs");
    const d = Math.round(h / 24);
    return d + "d";
  }

  // The chip's exact form, named once so the draw and the tick cannot drift into
  // rendering it two different ways.
  function fmtLeftParen(ms) { return " (" + fmtLeft(ms) + ")"; }

  // How close to a rollover counts as close, when the row has not said.
  //
  // A row that knows its own length sends soon_secs (UsageWindow.SoonSecs) and
  // that wins — CLAUDE's 5-hour window asks for half an hour, its weeks for two,
  // which is the same "one working stretch left" measured against very different
  // spans. Half an hour is the fallback because it is the tighter of the two: a
  // row whose length is unknown is likelier to be short, and warning late on a
  // long window is a smaller error than warning through the last day of one.
  //
  // Either way the countdown is keyed off resets_at, like the text it colors, so
  // it lands on exactly the rows that have a rollover to be near. The HOST rows
  // have no such instant and cannot reach it at all.
  const USAGE_SOON_MS = 30 * 60 * 1000;

  function usageSoonMs(w) {
    const secs = Number(w && w.soon_secs);
    return secs > 0 ? secs * 1000 : USAGE_SOON_MS;
  }

  // Write a countdown into its span and color it for how near the rollover is.
  //
  // One function for both the row and the folded chip, and for both the draw and
  // the 10s tick, because the class has to be re-decided every time the text is:
  // a row drawn with 35 minutes left crosses its threshold with no push behind
  // it, and a label that only took its color at draw time would sit muted
  // through the whole stretch it is meant to mark. The threshold rides on the
  // element for that reason — the tick walks the DOM and has no window object to
  // ask a second time.
  function setUsageLeft(el, ms, soonMs, fmt) {
    el.dataset.soon = String(soonMs);
    el.textContent = fmt(ms);
    // <= rather than <: a window at or past its instant (fmtUntil's "resetting")
    // is the extreme of the same state, not a return to calm — and it stays
    // stamped that way until the next reading brings a fresh resets_at.
    el.classList.toggle("soon", ms <= soonMs);
  }

  // renderUsage takes a new reading: it stores the message, settles the refresh
  // control and the age label, then draws. Folding a group re-draws WITHOUT
  // coming through here — a click is not a reading, and running usageIdle() on
  // one would stop the spinner on a refresh still in flight.
  function renderUsage(msg) {
    usageMsg = msg || null;
    // Any reading answers a pending refresh: the poller pushes every reading it
    // takes, so the next one to arrive IS the one the click asked for.
    usageIdle();
    renderUsageAge();
    drawUsage();
  }

  // drawUsage paints the section as one subsection per group: a heading, its
  // rows, and the caption that explains them. The server decides what the groups
  // are, what each row is called and what the caption says, because the set of
  // providers and the meters each one reports is the server's to know — this
  // walks the list rather than naming anything in it.
  //
  // A folded group builds its heading and nothing else, the way renderPaneList
  // skips a collapsed workspace's rows: the rows and the caption below them are
  // one subsection, so the caption folds with the numbers it annotates.
  function drawUsage() {
    usageListEl.innerHTML = "";
    usageGroupIDs = [];
    if (usageMsg) {
      let drew = false;
      for (const g of (usageMsg.groups || [])) {
        const wins = g.windows || [];
        // Never a bare heading: a subsection with nothing under it reads as a
        // broken section rather than as a provider with nothing to report.
        if (!wins.length && !g.note) continue;

        // The host groups are the ones the server measures rather than reads
        // from a provider, and the ones whose percentages mean something else: a
        // machine 70% into its RAM is a couple of test runs from swapping, while
        // a week 70% spent is on track. Their rows do not even share a scale
        // with each other, so they are looked up by name — the only group whose
        // row names this file is entitled to know, because this file's own
        // server chose them. Every other id is opaque here, and its rows take
        // the rate-limit scale.
        //
        // "host" is this machine; "host:<id>" is a cathost reporting its own
        // box. Same rows, same names, same scales — the prefix is what says the
        // numbers describe somewhere else.
        const host = g.id === "host" || (g.id || "").startsWith("host:");
        const levelsFor = (w) => host ? (HOST_LEVELS[w.name] || MEMORY_LEVELS) : USAGE_LEVELS;

        const gid = usageGroupID(g);
        usageGroupIDs.push(gid);
        const collapsed = usageCollapsed.has(gid);
        // The rule tracks what was EMITTED, not the index. A group skipped by
        // the check above would otherwise leave the next one drawing a hairline
        // as the first thing in the section, under the heading's own border.
        usageListEl.appendChild(usageGroupEl(g, gid, wins, levelsFor, collapsed, drew));
        drew = true;
        if (collapsed) continue;

        for (const w of wins) usageListEl.appendChild(usageRow(w.name, w, levelsFor(w)));

        // The caption sits directly under the rows it is about and above the
        // next heading, so it never reads as covering the whole section — only
        // some groups are showing an estimate, and only they explain themselves.
        if (g.note) {
          const li = document.createElement("li");
          li.className = "unote";
          li.textContent = g.note;
          usageListEl.appendChild(li);
        }
      }
      if (drew) return;
      // Nothing at all was drawable — no credential, no other provider, no
      // readable memory. Fall through to the pending state rather than leave an
      // empty section, which would look like a render that failed.
    }
    const li = document.createElement("li");
    li.className = "empty"; li.textContent = "…";
    usageListEl.appendChild(li);
  }

  // What a folded group is remembered by. The id is the server's stable handle
  // ("host", a provider key); the display name is the fallback for a server that
  // sends one without the other, so the fold survives a re-render either way.
  function usageGroupID(g) { return g.id || g.name || "—"; }

  // usageGroupEl builds one provider's header row — the click target that folds
  // and unfolds it, mirroring paneGroupEl.
  //
  // Folded, it stands in for the meters it hid by carrying ONE of them (see
  // usageHeadline), and its hover carries all of them (usageGroupTitle). That
  // split is the point: the fold is a request for one number, not for a digest,
  // and the reading that was folded away is still one hover from being read.
  // Expanded there is no summary — the rows say it better, and repeating a row
  // in the header would only be the same number twice.
  function usageGroupEl(g, gid, wins, levelsFor, collapsed, sep) {
    const li = document.createElement("li");
    li.className = "ugrp" + (sep ? " sep" : "");
    const name = document.createElement("span");
    name.textContent = g.name || g.id || "—";
    li.appendChild(name);

    const head = collapsed ? usageHeadline(wins) : null;
    if (head) {
      // Graded against the headline row's OWN scale: 82% of the disk is calm
      // where 82% of a rate window is not, so the chip's color comes from the
      // scale of the row it is quoting and from nothing else. A row with no
      // denominator (claude's local estimate) has a figure instead of a
      // percentage, and shows it — a token count is still the 5-hour answer.
      const lv = levelsFor(head);
      const pct = typeof head.pct === "number" ? head.pct : -1;
      if (pct >= lv.crit) li.classList.add("crit");
      else if (pct >= lv.high) li.classList.add("high");
      const text = pct >= 0 ? Math.round(pct) + "%" : (head.detail || "");
      if (text) {
        const s = document.createElement("span");
        s.className = "gsum";
        s.textContent = text;
        // A percentage of a window is half an answer: 3% spent means one thing
        // with four hours left in the five and quite another with ten minutes,
        // because what is left is what the rest of the afternoon has to fit
        // into. So when the quoted row is a window with a known rollover, the
        // chip carries both — "3% (2hrs)".
        //
        // Keyed off resets_at rather than off the row's name: a row that rolls
        // over is exactly the row a horizon belongs on, and the HOST rows have
        // no such instant (nothing resets memory but a process exiting), so
        // they keep the bare figure without this needing to know they exist.
        const at = head.resets_at ? Date.parse(head.resets_at) : NaN;
        if (!Number.isNaN(at)) {
          const left = document.createElement("span");
          left.className = "gleft";
          left.dataset.at = String(at); // absolute, so the tick can recompute it
          // The chip quotes one row, so it takes that row's horizon too: folded,
          // CLAUDE is its 5-hour window and warns on the half hour, not on the
          // week's two.
          setUsageLeft(left, at - Date.now(), usageSoonMs(head), fmtLeftParen);
          s.appendChild(left);
        }
        // No title of its own: the chip sits inside the heading, and a tooltip
        // here would shadow the heading's — which is the one that lists every
        // row the fold hid.
        li.appendChild(s);
      }
    }

    const car = document.createElement("span");
    car.className = "car"; car.textContent = collapsed ? "▶" : "▼";
    li.appendChild(car);
    li.title = usageGroupTitle(g, wins, levelsFor, collapsed);
    li.addEventListener("click", () => {
      if (collapsed) usageCollapsed.delete(gid); else usageCollapsed.add(gid);
      saveUsageCollapsed();
      drawUsage();
    });
    return li;
  }

  // The row a folded group speaks for.
  //
  // The server marks it (window.headline), because which of a provider's meters
  // answers the group's question is knowledge about the provider: Claude's
  // 5-hour window is what a long afternoon walks into, where its week is planned
  // around; the host's memory is what stops a session, where its CPU is usually
  // just the work. A front-end that picked for itself would have to enumerate
  // providers, which is the one thing this section is built not to do.
  //
  // Nothing marked falls back to the worst row by percentage — what every group
  // showed before headlines existed, and still the right answer for a provider
  // that has not said which row matters. Rows with no denominator cannot be
  // ranked and are skipped there; a group of only those folds with no chip at
  // all, which is honest.
  function usageHeadline(wins) {
    for (const w of wins) if (w.headline) return w;
    let peak = null;
    for (const w of wins) {
      if (typeof w.pct !== "number" || w.pct < 0) continue;
      if (!peak || w.pct > peak.pct) peak = w;
    }
    return peak;
  }

  // The group heading's hover: every row it holds, on screen or not.
  //
  // Folded this is the only way to read what the fold hid, and it is what lets
  // the chip beside it be a single chosen row rather than a compressed digest of
  // all of them — CLAUDE folds to its 5-hour number and the hover still has the
  // week and the per-model week; HOST folds to memory and the hover still has
  // disk and CPU. Expanded it duplicates the rows below, and is kept anyway: a
  // heading whose tooltip depended on fold state would be a worse thing to
  // learn than a few repeated lines.
  //
  // Built at draw time, so its countdowns age until the next reading lands —
  // two minutes at the poll's cadence, against a tooltip nobody holds open that
  // long. The rows themselves are the ones ticked every 10s.
  function usageGroupTitle(g, wins, levelsFor, collapsed) {
    const lines = [];
    for (const w of wins) {
      const lv = levelsFor(w);
      const pct = typeof w.pct === "number" ? w.pct : -1;
      let line = (w.name || "—") + ": ";
      if (pct >= 0) {
        line += pct.toFixed(1) + "% " + lv.of + (w.detail ? " (" + w.detail + ")" : "");
      } else {
        line += (w.detail || "unknown") + " (counted locally — no allowance on record)";
      }
      const at = w.resets_at ? Date.parse(w.resets_at) : NaN;
      if (!Number.isNaN(at)) line += " · " + fmtUntil(at - Date.now());
      lines.push(line);
    }
    if (!lines.length) lines.push("nothing to report");
    if (g.note) lines.push(g.note);
    lines.push((collapsed ? "click to expand" : "click to collapse"));
    return lines.join("\n");
  }

  // Where a row turns yellow, then red. Each kind of row keeps its own scale
  // because the same percentage does not mean the same thing on each of them:
  //
  //   window  a week 80% spent is on track — it is measured against a clock
  //           that will reset it.
  //   memory  a machine 80% into its RAM is a couple of test runs from
  //           swapping, and nothing resets it but a process exiting.
  //   disk    a volume 80% full is unremarkable and stays that way for weeks;
  //           it is the last few percent that end a build mid-write, so the
  //           warning waits and the alarm sits higher than either of the above.
  //   cpu     a machine at 80% is doing the work it was asked to do, and every
  //           build and test run touches 100% on the way past. The scale is the
  //           loosest of the four for that reason: it warns only where the row
  //           has stopped describing work and started describing a queue, and
  //           the strip under it is what actually carries the story.
  //
  // Several of the numbers happen to coincide today. The scales stay separate
  // so any of them can move without dragging the others with it.
  const USAGE_LEVELS  = { high: 75, crit: 90, of: "of the window used" };
  const MEMORY_LEVELS = { high: 80, crit: 90, of: "of host memory in use" };
  const DISK_LEVELS   = { high: 85, crit: 95, of: "of the disk in use" };
  const CPU_LEVELS    = { high: 90, crit: 98, of: "of the host's CPU busy" };

  // The HOST group's rows, by the name the server gives each one. An unknown
  // name falls back to the memory scale at the call site — a new host row is far
  // likelier to be another "nothing resets this" resource than a rate window.
  const HOST_LEVELS = { Memory: MEMORY_LEVELS, CPU: CPU_LEVELS, Disk: DISK_LEVELS };

  function usageRow(name, w, levels) {
    w = w || {};
    levels = levels || USAGE_LEVELS;
    const li = document.createElement("li");
    // pct < 0 is "no denominator" — a locally counted row (claude's transcript
    // estimate, every copilot row) has a figure but no allowance to measure it
    // against, so the row shows the figure and the CSS drops the meter rather
    // than drawing an empty one.
    const pct = typeof w.pct === "number" ? w.pct : -1;
    const known = pct >= 0;
    if (!known) li.classList.add("unknown");
    else if (pct >= levels.crit) li.classList.add("crit");
    else if (pct >= levels.high) li.classList.add("high");

    const row = document.createElement("div"); row.className = "urow";
    const nm = document.createElement("span"); nm.className = "uname"; nm.textContent = name;
    row.appendChild(nm);

    const at = w.resets_at ? Date.parse(w.resets_at) : NaN;
    if (!Number.isNaN(at)) {
      const r = document.createElement("span"); r.className = "ureset";
      r.dataset.at = String(at); // absolute: the push won't come again until the number moves
      setUsageLeft(r, at - Date.now(), usageSoonMs(w), fmtUntil);
      row.appendChild(r);
    } else if (known && w.detail) {
      // A row with both a percentage and a figure — host memory's "16.7G/24.0G"
      // — puts the figure where a countdown would go. The percentage is what
      // the row is scanned for; the absolute pair is what a worrying percentage
      // gets checked against, and it never needs re-rendering on the tick.
      const d = document.createElement("span"); d.className = "ureset";
      d.textContent = w.detail;
      row.appendChild(d);
    }

    const val = document.createElement("span"); val.className = "uval";
    val.textContent = known ? Math.round(pct) + "%" : (w.detail || "—");
    row.appendChild(val);
    li.appendChild(row);

    const meter = document.createElement("div"); meter.className = "umeter";
    const fill = document.createElement("i");
    fill.style.width = known ? Math.min(100, Math.max(0, pct)) + "%" : "0";
    meter.appendChild(fill);
    li.appendChild(meter);

    // A row the server sent a history for gets it drawn under the meter. Only
    // the rows whose movement between polls IS the information carry one (host
    // CPU today), so this is a capability of the row rather than of the section
    // — nothing here knows or asks which row it is looking at.
    if (Array.isArray(w.spark) && w.spark.length > 1) li.appendChild(usageSpark(w.spark));

    li.title = known
      ? name + ": " + pct.toFixed(1) + "% " + levels.of + (w.detail ? " (" + w.detail + ")" : "")
      : name + ": " + (w.detail || "unknown") + " (counted locally — no allowance on record to measure against)";
    return li;
  }

  // usageSpark draws a row's recent history as a filled line, oldest at the
  // left. The meter above it answers "where is it now"; this answers "where has
  // it been", which for a number that moves in seconds under a poll that fires
  // every couple of minutes is the only honest thing a single sample could not
  // have told you.
  //
  //   100% ┤       ╭─╮
  //        │   ╭───╯ ╰──╮        ← ten minutes, one point per sample
  //     0% ┼───╯        ╰────
  //        oldest          now
  //
  // The geometry is fixed at 100×20 user units and stretched to whatever width
  // the sidebar happens to be (preserveAspectRatio="none"), so no measurement of
  // the DOM is needed and a resized sidebar needs no redraw. That stretch is
  // non-uniform, which would smear a stroke — hence vector-effect, which keeps
  // the line 1px wide however far the box is pulled. The baseline sits at 20 and
  // the top of the plot at 1, so a sample at 100% still shows its stroke inside
  // the box rather than clipped in half by the edge.
  function usageSpark(samples) {
    const n = samples.length;
    const x = (i) => (i / (n - 1)) * 100;
    const y = (v) => 20 - (Math.min(100, Math.max(0, Number(v) || 0)) / 100) * 19;
    let d = "";
    for (let i = 0; i < n; i++) d += (i ? "L" : "M") + x(i).toFixed(2) + " " + y(samples[i]).toFixed(2);

    const svg = document.createElementNS(SVGNS, "svg");
    svg.setAttribute("class", "uspark");
    svg.setAttribute("viewBox", "0 0 100 20");
    svg.setAttribute("preserveAspectRatio", "none");
    const fill = document.createElementNS(SVGNS, "path");
    fill.setAttribute("class", "sfill");
    // Closed down to the baseline at both ends: the area is what makes the shape
    // legible at 20px, where a bare 1px line reads as noise.
    fill.setAttribute("d", d + "L100 20L0 20Z");
    const line = document.createElementNS(SVGNS, "path");
    line.setAttribute("class", "sline");
    line.setAttribute("d", d);
    line.setAttribute("vector-effect", "non-scaling-stroke");
    line.setAttribute("stroke-width", "1");
    line.setAttribute("stroke-linejoin", "round");
    svg.appendChild(fill); svg.appendChild(line);
    return svg;
  }

  // The two labels that move on their own between pushes — the reset countdowns
  // and the age of the reading — share one tick, which rewrites them rather than
  // re-rendering the rows. Ten seconds rather than the countdown's old thirty:
  // the age starts at zero after every push, and a "0s ago" that lingers half a
  // minute is the one stale number this section exists to prevent.
  setInterval(() => {
    renderUsageAge();
    // Only the stamped ones: the same slot also carries the HOST rows' absolute
    // figures, which are facts rather than countdowns and have no instant to
    // recompute from. [data-at] is what separates the two.
    for (const el of usageListEl.querySelectorAll(".ureset[data-at]")) {
      // data-soon was stamped at draw from the row's own horizon; re-reading it
      // keeps the tick free of any knowledge of which row it is rewriting.
      setUsageLeft(el, Number(el.dataset.at) - Date.now(), Number(el.dataset.soon), fmtUntil);
    }
    // The folded chip's half of the same countdown. It is only in the DOM while
    // the group is folded, so the query is usually empty — and when it is not,
    // this is the one number on screen for that provider, which makes it the
    // last one that should be allowed to age.
    for (const el of usageListEl.querySelectorAll(".gleft[data-at]")) {
      setUsageLeft(el, Number(el.dataset.at) - Date.now(), Number(el.dataset.soon), fmtLeftParen);
    }
  }, 10000);


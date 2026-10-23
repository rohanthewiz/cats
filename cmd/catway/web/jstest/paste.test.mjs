// Native paste routing in onPaste (21-clipboard.js).
//
// A paste aimed at a dialog field must stay in that field: the New Workspace
// dialog used to lose ⌘V to the pane underneath, because the document-level
// listener sent every paste to the focused pane. A paste with the text sink
// (or nothing in particular) focused is the terminal's, and still goes to the
// pane. While a dialog or menu is open, nothing is pasted into the pane.

import { loadFns, eq, ok, report } from "./testutil.mjs";

const textSinkEl = { tagName: "TEXTAREA" };

function world({ open = false } = {}) {
  const sent = [];
  const f = loadFns({
    files: ["21-clipboard.js"],
    names: ["onPaste", "isEditableField"],
    stubs: ["clipRead", "toast"],
    env: {
      chatEl: { contains: () => false },
      textSinkEl,
      uiOpen: () => open,
      sendMsg: (m) => sent.push(m),
      window: {},
      document: { addEventListener: () => {} },
    },
  });
  return { f, sent };
}

function paste(target, text = "hello") {
  const e = { target, prevented: false, clipboardData: { getData: () => text } };
  e.preventDefault = () => { e.prevented = true; };
  return e;
}

// ---- the text sink is the terminal: its paste goes to the pane ----------------

{
  const { f, sent } = world();
  const e = paste(textSinkEl);
  f.onPaste(e);
  ok(e.prevented, "a paste into the text sink is taken from the browser");
  eq(sent, [{ t: "paste", data: "hello" }], "and sent to the focused pane");
}

// ---- a dialog's text input keeps its paste ------------------------------------

{
  const { f, sent } = world({ open: true });
  const input = { tagName: "INPUT", type: "text" };
  const e = paste(input);
  f.onPaste(e);
  ok(!e.prevented, "a paste into a dialog input is left to the browser");
  eq(sent.length, 0, "and nothing reaches the pane");
}

// The field test stands on its own, not only through uiOpen: an input outside
// any modal (a sidebar filter, say) keeps its paste too.
{
  const { f, sent } = world({ open: false });
  for (const el of [{ tagName: "INPUT" }, { tagName: "TEXTAREA" }, { tagName: "DIV", isContentEditable: true }]) {
    const e = paste(el);
    f.onPaste(e);
    ok(!e.prevented, `a paste into ${el.tagName} stays local`);
  }
  eq(sent.length, 0, "no editable field leaks its paste to the pane");
}

// ---- an open dialog swallows a paste aimed at a non-text control ---------------

{
  const { f, sent } = world({ open: true });
  const e = paste({ tagName: "BUTTON" });
  f.onPaste(e);
  eq(sent.length, 0, "with a dialog open, a paste on its button does not reach the pane");
}

// ---- isEditableField: what the browser would actually paste into ---------------

{
  const { f } = world();
  ok(!f.isEditableField({ tagName: "INPUT", type: "checkbox" }), "a checkbox is not a text field");
  ok(!f.isEditableField({ tagName: "INPUT", type: "text", readOnly: true }), "a read-only input is not");
  ok(!f.isEditableField({ tagName: "TEXTAREA", disabled: true }), "a disabled textarea is not");
  ok(f.isEditableField({ tagName: "INPUT", type: "search" }), "a search input is");
  ok(!f.isEditableField(null), "no target is not");
}

// ---- body focus (no field) with no dialog: the pane's, as before ----------------

{
  const { f, sent } = world();
  const e = paste({ tagName: "BODY" });
  f.onPaste(e);
  eq(sent, [{ t: "paste", data: "hello" }], "a paste with nothing focused goes to the pane");
  const empty = paste({ tagName: "BODY" }, "");
  f.onPaste(empty);
  ok(!empty.prevented, "an empty clipboard is left alone");
}

report("paste");

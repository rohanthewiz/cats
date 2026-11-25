// A workspace from a repository (27-dialogs.js repoSpec / createWorkspace).
//
// What is pinned:
//   - repoSpec previews the same URL and folder name gitclone.Resolve (Go)
//     will produce — the GitHub shorthand above all — and answers null for
//     text that is not a repository yet;
//   - createWorkspace sends `repo` only when one was given, announces the
//     clone, and carries the repo through the "create folder?" retry, since
//     the retry is the same create.

import { loadFns, eq, ok, report } from "./testutil.mjs";

const sent = [], toasts = [];
let confirm = null;
let replies = [];
const { repoSpec, createWorkspace } = loadFns({
  files: ["27-dialogs.js"],
  names: ["repoSpec", "createWorkspace"],
  env: {
    toast: (m) => toasts.push(m),
    dialogConfirm: (o) => { confirm = o; },
    startPluginInWorkspace: () => {},
    sendCmdAwait: (name, params, cb) => { sent.push({ name, params }); cb(replies.shift() || { ok: true }); },
  },
});

const gh = { url: "https://github.com/rohanthewiz/cdx.git", name: "cdx" };
eq(repoSpec("rohanthewiz/cdx"), gh, "owner/repo is GitHub over https");
eq(repoSpec("  rohanthewiz/cdx.git "), gh, "a typed .git is not doubled");
eq(repoSpec("github.com/rohanthewiz/cdx/"), gh, "a URL pasted without its scheme");
eq(repoSpec("git@github.com:rohanthewiz/cdx.git"),
  { url: "git@github.com:rohanthewiz/cdx.git", name: "cdx" }, "scp-style passes through");
eq(repoSpec("https://gitlab.com/group/sub/proj.git"),
  { url: "https://gitlab.com/group/sub/proj.git", name: "proj" }, "a full URL passes through");
eq(repoSpec("/srv/git/thing.git"), { url: "/srv/git/thing.git", name: "thing" }, "a local path passes through");
for (const bad of ["", "   ", "rohanthewiz", "rohanthewiz/", "a/b/c", "--upload-pack=x", "a b/c"]) {
  eq(repoSpec(bad), null, "not a repository: " + JSON.stringify(bad));
}

// No repo: the params are exactly what they were before the field existed.
createWorkspace("n", "/p", null, "", "");
eq(sent.at(-1), { name: "workspace.create", params: { name: "n", path: "/p", host: "" } }, "no repo, no repo param");
eq(toasts.length, 0, "and nothing to announce");

// A repo rides the create, and the wait is announced.
createWorkspace("", "~/projs", null, "", " rohanthewiz/cdx ");
eq(sent.at(-1).params, { name: "", path: "~/projs", host: "", repo: "rohanthewiz/cdx" }, "repo is sent, trimmed");
ok(toasts.at(-1).startsWith("cloning rohanthewiz/cdx"), "the clone is announced");

// A missing parent folder is offered, and the retry is the same create.
replies = [{ ok: false, error: "no such directory: /new/place" }];
createWorkspace("", "/new/place", null, "", "a/b");
ok(confirm && confirm.message.includes("clone into it"), "the offer says what the folder is for");
confirm.onConfirm();
eq(sent.at(-1).params, { name: "", path: "/new/place", host: "", repo: "a/b", mkdir: true }, "the retry keeps the repo");

report("repoworkspace");

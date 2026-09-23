package wire

import "testing"

// The plugin-type vocabulary: which words a manifest may declare, and the two
// questions every consumer asks of a pane — is it an editor, and can a prompt
// be dropped into it.

func TestValidPluginType(t *testing.T) {
	for _, typ := range []string{"", PluginTypeAgent, PluginTypeEditor, PluginTypeTodosMgr, PluginTypeNotesMgr, PluginTypeHTTPClient, PluginTypeGit, "dev_server"} {
		if !ValidPluginType(typ) {
			t.Errorf("%q should be a valid plugin type", typ)
		}
	}
	for _, typ := range []string{"Editor", "todos-mgr", "notes mgr", "_x", "1st"} {
		if ValidPluginType(typ) {
			t.Errorf("%q should be refused", typ)
		}
	}
}

// The config names editors regardless of the manifest, and a manifest that
// declares "editor" is honoured when the config does not list it. Anything else
// declared passes through, and is not an editor.
func TestResolvePluginType(t *testing.T) {
	ed := EditorInfo{Agents: []string{"ced"}}
	cases := []struct {
		agent, declared, want string
		editor                bool
	}{
		{"ced", "", PluginTypeEditor, true},                 // typed into a shell, or a pre-type manifest
		{"CEd", PluginTypeTodosMgr, PluginTypeEditor, true}, // the config wins, case-insensitively
		{"vimx", PluginTypeEditor, PluginTypeEditor, true},  // declared, never listed
		{"", PluginTypeEditor, PluginTypeEditor, true},      // a quiet editor pane
		{"claude", "", "", false},                           // a coding agent
		{"claude", PluginTypeTodosMgr, PluginTypeTodosMgr, false},
		{"", PluginTypeNotesMgr, PluginTypeNotesMgr, false},
		{"", "dev_server", "dev_server", false}, // unknown types pass through
	}
	for _, c := range cases {
		typ, editor := ed.ResolvePluginType(c.agent, c.declared)
		if typ != c.want || editor != c.editor {
			t.Errorf("ResolvePluginType(%q, %q) = %q, %v; want %q, %v", c.agent, c.declared, typ, editor, c.want, c.editor)
		}
	}
}

// A drop target is a detected agent that is not a tool plugin. An unset type
// is an agent (every claude pane started from a shell, and every pane from a
// cats too old to send the field); an unknown type is a tool.
func TestPaneMetaIsDropAgent(t *testing.T) {
	cases := []struct {
		meta PaneMeta
		want bool
	}{
		{PaneMeta{Agent: "claude"}, true},
		{PaneMeta{Agent: "claude", PluginType: PluginTypeAgent}, true},
		{PaneMeta{Agent: "ced", PluginType: PluginTypeEditor}, false},
		{PaneMeta{Agent: "x", PluginType: PluginTypeTodosMgr}, false},
		{PaneMeta{Agent: "x", PluginType: "dev_server"}, false},
		{PaneMeta{PluginType: PluginTypeAgent}, false}, // no agent detected: nothing to take a prompt
		{PaneMeta{}, false},
	}
	for _, c := range cases {
		if got := c.meta.IsDropAgent(); got != c.want {
			t.Errorf("IsDropAgent(%+v) = %v, want %v", c.meta, got, c.want)
		}
	}
}

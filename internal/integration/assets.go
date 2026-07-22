package integration

import _ "embed"

// The assets are embedded byte-for-byte from the Rust tree (no install-time
// templating): each carries its own "managed by cats" banner plus
// CATS_INTEGRATION_ID / CATS_INTEGRATION_VERSION markers that install and
// status logic key off. The .ps1 variants in the tree are Windows-only and are
// deliberately not embedded (the Go port is unix-only; see targetSupported).
//
// Explicit per-file //go:embed directives are required anyway: a bare
// directory pattern would skip hermes/__init__.py (underscore-prefixed).

//go:embed assets/pi/cats-agent-state.ts
var piExtensionAsset string

//go:embed assets/omp/cats-agent-state.ts
var ompExtensionAsset string

//go:embed assets/claude/cats-agent-state.sh
var claudeHookAsset string

//go:embed assets/codex/cats-agent-state.sh
var codexHookAsset string

//go:embed assets/kimi/cats-agent-state.sh
var kimiHookAsset string

//go:embed assets/copilot/cats-agent-state.sh
var copilotHookAsset string

//go:embed assets/droid/cats-agent-state.sh
var droidHookAsset string

//go:embed assets/opencode/cats-agent-state.js
var opencodePluginAsset string

//go:embed assets/kilo/cats-agent-state.js
var kiloPluginAsset string

//go:embed assets/hermes/plugin.yaml
var hermesPluginManifestAsset string

//go:embed assets/hermes/__init__.py
var hermesPluginInitAsset string

//go:embed assets/qodercli/cats-agent-state.sh
var qodercliHookAsset string

//go:embed assets/cursor/cats-agent-state.sh
var cursorHookAsset string

// protocol.go is the typed ACP v1 vocabulary this client actually uses —
// deliberately a subset. The protocol defines far more (fs/*, terminal/*,
// modes, config options); we type only what we send or read, and everything
// else stays json.RawMessage so an agent's extra fields can never break
// decoding. Shapes verified against the ACP v1 schema and a live probe of
// GitHub Copilot CLI 1.0.77.
package acp

import "encoding/json"

// ProtocolVersion is the ACP major version this client speaks. Copilot 1.0.77
// answers with 1; v2 is still pre-release.
const ProtocolVersion = 1

// --- initialize ---------------------------------------------------------------

// ClientCapabilities declares which agent→client services we serve. All false
// in cats: copilot has its own file and shell tools, and the spec forbids an
// agent from calling fs/* or terminal/* when the capability is absent — so
// declaring nothing means those requests never arrive and only
// session/request_permission needs a handler.
type ClientCapabilities struct {
	FS struct {
		ReadTextFile  bool `json:"readTextFile"`
		WriteTextFile bool `json:"writeTextFile"`
	} `json:"fs"`
	Terminal bool `json:"terminal"`
}

// ClientInfo names the client in the agent's logs/telemetry. Version is not
// omitempty because copilot validates it as a required string — an absent
// version fails the whole initialize with -32602.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams is the first request on a new connection.
type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         ClientInfo         `json:"clientInfo"`
}

// InitializeResult keeps the capability blobs raw: we never branch on them in
// v1, and raw bytes are loggable evidence when an agent misbehaves.
type InitializeResult struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities,omitempty"`
	AuthMethods       json.RawMessage `json:"authMethods,omitempty"`
}

// --- session/new --------------------------------------------------------------

// NewSessionParams starts a conversation. MCPServers is a required field in
// the schema — always send the empty slice, never nil, so it marshals as []
// rather than null.
type NewSessionParams struct {
	Cwd        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

// ModelInfo is one entry of the agent's model roster.
type ModelInfo struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name,omitempty"`
}

// NewSessionResult carries the session id and, from some agents, the model
// roster. Copilot's roster shape follows ced's observed
// models.availableModels/currentModelId; absent fields decode to zero values.
type NewSessionResult struct {
	SessionID string `json:"sessionId"`
	Models    struct {
		Available      []ModelInfo `json:"availableModels"`
		CurrentModelID string      `json:"currentModelId"`
	} `json:"models"`
}

// --- session/prompt -----------------------------------------------------------

// ContentBlock is a prompt or update content item. v1 sends and renders only
// text; the other block types (image, resource) are ignored on read via
// ContentText.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// PromptParams is one user turn.
type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// Stop reasons a session/prompt response may carry.
const (
	StopEndTurn   = "end_turn"
	StopCancelled = "cancelled"
	StopRefusal   = "refusal"
)

// PromptResult ends a turn. The response arrives only when the agent is done
// (or cancelled — the spec requires a normal response with stopReason
// "cancelled", not a JSON-RPC error).
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// CancelParams is the session/cancel notification payload.
type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// --- session/update -----------------------------------------------------------

// SessionNotification is the envelope of a session/update notification.
type SessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// Update discriminator values this client acts on; every other variant
// (thoughts, plans, command/mode/config/usage updates) is deliberately
// dropped in v1.
const (
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateToolCall          = "tool_call"
	UpdateToolCallUpdate    = "tool_call_update"
)

// UpdateEnvelope flattens the fields of the variants we handle. The
// discriminator is SessionUpdate; unused fields simply stay zero for other
// variants, which is what lets one struct serve the whole switch.
type UpdateEnvelope struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"`    // *_chunk: a ContentBlock
	ToolCallID    string          `json:"toolCallId,omitempty"` // tool_call / tool_call_update
	Title         string          `json:"title,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Status        string          `json:"status,omitempty"`
}

// ContentText extracts the text of a ContentBlock, or "" for non-text blocks.
func ContentText(raw json.RawMessage) string {
	var b ContentBlock
	if err := json.Unmarshal(raw, &b); err != nil || b.Type != "text" {
		return ""
	}
	return b.Text
}

// --- session/request_permission -----------------------------------------------

// PermOption is one choice the agent offers for a permission request. Kind is
// one of allow_once, allow_always, reject_once, reject_always.
type PermOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// ParsePermission pulls the display fields and options out of a
// session/request_permission params blob.
func ParsePermission(params json.RawMessage) (sessionID, toolTitle, toolKind string, options []PermOption) {
	var p struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"toolCall"`
		Options []PermOption `json:"options"`
	}
	_ = json.Unmarshal(params, &p)
	return p.SessionID, p.ToolCall.Title, p.ToolCall.Kind, p.Options
}

// permOutcome is the response shape: a doubly-nested outcome object,
// {"outcome":{"outcome":"selected","optionId":...}} — as the schema defines
// and copilot expects.
type permOutcome struct {
	Outcome struct {
		Outcome  string `json:"outcome"`
		OptionID string `json:"optionId,omitempty"`
	} `json:"outcome"`
}

// SelectedOutcome answers a permission request with the chosen option.
func SelectedOutcome(optionID string) any {
	var o permOutcome
	o.Outcome.Outcome = "selected"
	o.Outcome.OptionID = optionID
	return o
}

// CancelledOutcome answers a permission request that was overtaken by a turn
// cancellation — the spec requires pending permissions be resolved this way,
// or a well-behaved agent may never end the turn.
func CancelledOutcome() any {
	var o permOutcome
	o.Outcome.Outcome = "cancelled"
	return o
}

// RejectOutcome answers with the agent's own gentlest reject option: a
// one-time reject if offered, a permanent one otherwise, cancelled as the
// last resort. Used as the backstop when nobody answers a prompt before the
// turn ends.
func RejectOutcome(options []PermOption) any {
	for _, kind := range []string{"reject_once", "reject_always"} {
		for _, o := range options {
			if o.Kind == kind {
				return SelectedOutcome(o.OptionID)
			}
		}
	}
	return CancelledOutcome()
}

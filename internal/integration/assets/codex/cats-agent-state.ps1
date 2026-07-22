# installed by cats
# managed by cats; reinstalling or updating the integration overwrites this file.
# add custom hooks beside this file instead of editing it.
# CATS_INTEGRATION_ID=codex
# CATS_INTEGRATION_VERSION=5

param([string]$Action = "")

if ($Action -ne "session") { exit 0 }
if ($env:CATS_ENV -ne "1") { exit 0 }
if ([string]::IsNullOrWhiteSpace($env:CATS_PANE_ID)) { exit 0 }

$inputText = [Console]::In.ReadToEnd()
try {
    $payload = if ([string]::IsNullOrWhiteSpace($inputText)) { $null } else { $inputText | ConvertFrom-Json }
} catch {
    exit 0
}

$sessionId = $payload.session_id
if ([string]::IsNullOrWhiteSpace($sessionId)) { exit 0 }

$seq = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
try {
    & cats pane report-agent-session $env:CATS_PANE_ID --source cats:codex --agent codex --seq $seq --agent-session-id $sessionId 2>$null | Out-Null
} catch {
}

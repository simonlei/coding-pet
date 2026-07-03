# coding-pet-hook.ps1 -- CodeBuddy IDE Hook forwarder for Windows PowerShell.
#                        Reports session events to the coding-pet local agent.
#
# Official spec: https://www.codebuddy.cn/docs/ide/Features/Hooks
# CodeBuddy IDE feeds a JSON blob (with hook_event_name / session_id / cwd ...)
# via stdin, and expects a decision JSON (e.g. {"continue":true}) on stdout.
#
# This one script handles all 7 events (SessionStart / SessionEnd /
# UserPromptSubmit / PreToolUse / PostToolUse / Stop / PreCompact) uniformly:
#   1) forward the whole event to the local agent's /ide/hook endpoint;
#   2) always emit {"continue":true} so the IDE never blocks.
#
# Env vars:
#   DASHBOARD_IDE_HOOK_URL  override the report URL
#                           (default http://127.0.0.1:38765/ide/hook)
#   CODEBUDDY_PRODUCT       codebuddy | workbuddy (optional, for UI grouping)

$ErrorActionPreference = 'Continue'

$Url = if ($env:DASHBOARD_IDE_HOOK_URL) { $env:DASHBOARD_IDE_HOOK_URL } else { 'http://127.0.0.1:38765/ide/hook' }

# Read stdin.
$InputText = ''
try { $InputText = [Console]::In.ReadToEnd() } catch { $InputText = '' }
if ([string]::IsNullOrWhiteSpace($InputText)) { $InputText = '{}' }

# Parse JSON, fall back to $null on failure.
$obj = $null
try { $obj = $InputText | ConvertFrom-Json -ErrorAction Stop } catch { $obj = $null }

function Get-Field($o, [string]$key) {
    if ($null -eq $o) { return '' }
    if ($o.PSObject.Properties.Name -contains $key -and $null -ne $o.$key) {
        return [string]$o.$key
    }
    return ''
}

$eventName = Get-Field $obj 'hook_event_name'
$sessionId = Get-Field $obj 'session_id'
$cwd       = Get-Field $obj 'cwd'

$tool = 'codebuddy_ide'
if ($env:CODEBUDDY_PRODUCT -and $env:CODEBUDDY_PRODUCT.ToLower() -eq 'workbuddy') {
    $tool = 'workbuddy'
}

$payload = @{
    hook_event_name = $eventName
    session_id      = $sessionId
    cwd             = $cwd
    tool            = $tool
    timestamp       = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
} | ConvertTo-Json -Compress

# Forward to agent. 2s timeout, silent on failure, never block the IDE.
try {
    Invoke-RestMethod -Uri $Url -Method Post -ContentType 'application/json' -Body $payload -TimeoutSec 2 -ErrorAction SilentlyContinue | Out-Null
} catch {
    # ignore
}

# Emit the decision JSON required by the CodeBuddy Hooks spec.
Write-Output '{"continue":true}'
exit 0

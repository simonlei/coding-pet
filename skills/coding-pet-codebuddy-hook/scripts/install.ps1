# install.ps1 -- Install CodeBuddy IDE hooks for coding-pet dashboard (Windows).
#
# What it does:
#   1. Copy coding-pet-hook.ps1 to %USERPROFILE%\.codebuddy\hooks\
#   2. Backup existing settings.json to settings.json.bak-<ts> (if any)
#   3. Merge 7 hook entries (all tagged "__managed_by":"coding-pet") into
#      %USERPROFILE%\.codebuddy\settings.json, idempotently:
#         - remove any existing coding-pet entries first
#         - then add the fresh set
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File install.ps1
#   # optional flags:
#   #   -HookUrl  http://127.0.0.1:xxxx/ide/hook  (writes agent.env)
#   #   -DryRun                                    (print only, no writes)

[CmdletBinding()]
param(
    [string]$HookUrl = '',
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'

$MarkerKey   = '__managed_by'
$MarkerValue = 'coding-pet'
$Home_       = [Environment]::GetFolderPath('UserProfile')
$CbDir       = Join-Path $Home_ '.codebuddy'
$HookDir     = Join-Path $CbDir 'hooks'
$SettingsFp  = Join-Path $CbDir 'settings.json'
$ScriptDst   = Join-Path $HookDir 'coding-pet-hook.ps1'
$EnvFile     = Join-Path $HookDir 'agent.env'

$ScriptSrc   = Join-Path $PSScriptRoot 'coding-pet-hook.ps1'

function Write-Info($msg) { Write-Host "[install] $msg" }
function Write-Warn($msg) { Write-Host "[install] $msg" -ForegroundColor Yellow }

if (-not (Test-Path $ScriptSrc)) {
    throw "Source hook script not found: $ScriptSrc"
}

# ---- Step 1: prepare directories & copy hook script ----
if (-not (Test-Path $HookDir)) {
    if ($DryRun) { Write-Info "[dry-run] mkdir $HookDir" }
    else { New-Item -ItemType Directory -Path $HookDir -Force | Out-Null }
}

if ($DryRun) {
    Write-Info "[dry-run] copy $ScriptSrc -> $ScriptDst"
} else {
    Copy-Item -Path $ScriptSrc -Destination $ScriptDst -Force
    Write-Info "hook script installed: $ScriptDst"
}

# ---- Step 2: optional agent.env for custom URL ----
if ($HookUrl) {
    $envContent = "DASHBOARD_IDE_HOOK_URL=$HookUrl`r`n"
    if ($DryRun) {
        Write-Info "[dry-run] write agent.env with URL=$HookUrl"
    } else {
        Set-Content -Path $EnvFile -Value $envContent -Encoding utf8 -NoNewline
        Write-Info "custom hook URL saved: $EnvFile"
    }
}

# ---- Step 3: read + backup settings.json ----
$settings = $null
if (Test-Path $SettingsFp) {
    $raw = Get-Content -Path $SettingsFp -Raw -Encoding utf8
    if (-not [string]::IsNullOrWhiteSpace($raw)) {
        try { $settings = $raw | ConvertFrom-Json -ErrorAction Stop }
        catch {
            throw "Existing settings.json is not valid JSON. Fix or remove it first: $SettingsFp"
        }
    }
    $ts = Get-Date -Format 'yyyyMMdd-HHmmss'
    $bak = "$SettingsFp.bak-$ts"
    if ($DryRun) {
        Write-Info "[dry-run] backup $SettingsFp -> $bak"
    } else {
        Copy-Item -Path $SettingsFp -Destination $bak -Force
        Write-Info "backup created: $bak"
    }
}
if ($null -eq $settings) {
    $settings = [pscustomobject]@{}
}

# ---- Step 4: build the fresh 7 hook entries (all tagged) ----
$Command = "powershell -NoProfile -ExecutionPolicy Bypass -File `"%USERPROFILE%\.codebuddy\hooks\coding-pet-hook.ps1`""

function New-HookGroup([string]$matcher) {
    $inner = [pscustomobject]@{
        type    = 'command'
        command = $Command
        timeout = 5
    }
    $group = [ordered]@{}
    if ($matcher) { $group['matcher'] = $matcher }
    $group['hooks']       = @($inner)
    $group[$MarkerKey]    = $MarkerValue
    return [pscustomobject]$group
}

$fresh = [ordered]@{
    SessionStart     = @( New-HookGroup 'startup' )
    SessionEnd       = @( New-HookGroup 'other'   )
    UserPromptSubmit = @( New-HookGroup ''        )
    PreToolUse       = @( New-HookGroup '*'       )
    PostToolUse      = @( New-HookGroup '*'       )
    Stop             = @( New-HookGroup ''        )
    PreCompact       = @( New-HookGroup 'auto'    )
}

# ---- Step 5: merge (remove old coding-pet entries, then append fresh) ----
$existingHooks = $null
if ($settings.PSObject.Properties.Name -contains 'hooks' -and $null -ne $settings.hooks) {
    $existingHooks = $settings.hooks
} else {
    $existingHooks = [pscustomobject]@{}
}

# For each event we manage, filter out coding-pet-marked groups first.
$mergedHooks = [ordered]@{}
$eventNames = @($existingHooks.PSObject.Properties.Name) + @($fresh.Keys) | Select-Object -Unique
foreach ($ev in $eventNames) {
    $kept = @()
    if ($existingHooks.PSObject.Properties.Name -contains $ev -and $null -ne $existingHooks.$ev) {
        foreach ($grp in @($existingHooks.$ev)) {
            $isOurs = $false
            if ($null -ne $grp -and $grp.PSObject.Properties.Name -contains $MarkerKey) {
                if ("$($grp.$MarkerKey)" -eq $MarkerValue) { $isOurs = $true }
            }
            if (-not $isOurs) { $kept += $grp }
        }
    }
    if ($fresh.Contains($ev)) {
        $kept += $fresh[$ev]
    }
    if ($kept.Count -gt 0) {
        $mergedHooks[$ev] = $kept
    }
}

# Reassign hooks back onto $settings (preserve other top-level keys).
if ($settings.PSObject.Properties.Name -contains 'hooks') {
    $settings.hooks = [pscustomobject]$mergedHooks
} else {
    $settings | Add-Member -MemberType NoteProperty -Name 'hooks' -Value ([pscustomobject]$mergedHooks) -Force
}

$json = $settings | ConvertTo-Json -Depth 20
if ($DryRun) {
    Write-Info "[dry-run] would write settings.json:"
    Write-Host $json
} else {
    Set-Content -Path $SettingsFp -Value $json -Encoding utf8
    Write-Info "settings.json updated: $SettingsFp"
}

Write-Info "done. Please restart CodeBuddy IDE to activate the hooks."

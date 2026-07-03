# uninstall.ps1 -- Remove coding-pet-managed CodeBuddy IDE hooks (Windows).
#
# What it does:
#   1. Backup existing settings.json to settings.json.bak-<ts>
#   2. Remove any hook group tagged "__managed_by":"coding-pet"
#      from every event array; drop the event key entirely if empty.
#   3. Delete %USERPROFILE%\.codebuddy\hooks\coding-pet-hook.ps1 and agent.env.
#      (pass -KeepScripts to keep them.)
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File uninstall.ps1
#   # optional:
#   #   -KeepScripts   keep the hook script files
#   #   -DryRun

[CmdletBinding()]
param(
    [switch]$KeepScripts,
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

function Write-Info($msg) { Write-Host "[uninstall] $msg" }
function Write-Warn($msg) { Write-Host "[uninstall] $msg" -ForegroundColor Yellow }

# ---- Step 1: settings.json ----
if (Test-Path $SettingsFp) {
    $raw = Get-Content -Path $SettingsFp -Raw -Encoding utf8
    $settings = $null
    if (-not [string]::IsNullOrWhiteSpace($raw)) {
        try { $settings = $raw | ConvertFrom-Json -ErrorAction Stop }
        catch {
            throw "Existing settings.json is not valid JSON. Fix or remove it first: $SettingsFp"
        }
    }
    if ($null -ne $settings) {
        $ts = Get-Date -Format 'yyyyMMdd-HHmmss'
        $bak = "$SettingsFp.bak-$ts"
        if ($DryRun) {
            Write-Info "[dry-run] backup $SettingsFp -> $bak"
        } else {
            Copy-Item -Path $SettingsFp -Destination $bak -Force
            Write-Info "backup created: $bak"
        }

        if ($settings.PSObject.Properties.Name -contains 'hooks' -and $null -ne $settings.hooks) {
            $hooks    = $settings.hooks
            $newHooks = [ordered]@{}
            foreach ($prop in $hooks.PSObject.Properties) {
                $ev   = $prop.Name
                $kept = @()
                foreach ($grp in @($prop.Value)) {
                    $isOurs = $false
                    if ($null -ne $grp -and $grp.PSObject.Properties.Name -contains $MarkerKey) {
                        if ("$($grp.$MarkerKey)" -eq $MarkerValue) { $isOurs = $true }
                    }
                    if (-not $isOurs) { $kept += $grp }
                }
                if ($kept.Count -gt 0) { $newHooks[$ev] = $kept }
            }
            if ($newHooks.Count -eq 0) {
                # Remove the entire "hooks" key if nothing left.
                $settings.PSObject.Properties.Remove('hooks') | Out-Null
                Write-Info "hooks key emptied and removed"
            } else {
                $settings.hooks = [pscustomobject]$newHooks
                Write-Info "coding-pet-managed hook entries removed"
            }

            $json = $settings | ConvertTo-Json -Depth 20
            if ($DryRun) {
                Write-Info "[dry-run] would write settings.json:"
                Write-Host $json
            } else {
                Set-Content -Path $SettingsFp -Value $json -Encoding utf8
                Write-Info "settings.json updated: $SettingsFp"
            }
        } else {
            Write-Info "no 'hooks' key in settings.json, nothing to strip"
        }
    }
} else {
    Write-Info "settings.json not found, skip"
}

# ---- Step 2: delete hook script files ----
if ($KeepScripts) {
    Write-Info "keep-scripts flag set, hook script files retained"
} else {
    foreach ($fp in @($ScriptDst, $EnvFile)) {
        if (Test-Path $fp) {
            if ($DryRun) { Write-Info "[dry-run] delete $fp" }
            else {
                Remove-Item -Path $fp -Force
                Write-Info "deleted: $fp"
            }
        }
    }
    # Try to remove hooks dir if empty.
    if (Test-Path $HookDir) {
        $left = Get-ChildItem -Path $HookDir -Force | Measure-Object
        if ($left.Count -eq 0) {
            if ($DryRun) { Write-Info "[dry-run] remove empty dir $HookDir" }
            else {
                Remove-Item -Path $HookDir -Force
                Write-Info "removed empty dir: $HookDir"
            }
        }
    }
}

Write-Info "done. Restart CodeBuddy IDE to fully deactivate the hooks."

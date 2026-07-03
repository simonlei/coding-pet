#!/usr/bin/env bash
# uninstall.sh -- Remove coding-pet-managed CodeBuddy IDE hooks (Unix).
#
# Usage:
#   ./uninstall.sh
#   ./uninstall.sh --keep-scripts     # keep the hook script files
#   ./uninstall.sh --dry-run

set -eu

MARKER_KEY='__managed_by'
MARKER_VALUE='coding-pet'
CB_DIR="$HOME/.codebuddy"
HOOK_DIR="$CB_DIR/hooks"
SETTINGS_FP="$CB_DIR/settings.json"
SCRIPT_DST="$HOOK_DIR/coding-pet-hook.sh"
ENV_FILE="$HOOK_DIR/agent.env"

KEEP_SCRIPTS=0
DRY_RUN=0
while [ $# -gt 0 ]; do
    case "$1" in
        --keep-scripts) KEEP_SCRIPTS=1; shift;;
        --dry-run)      DRY_RUN=1; shift;;
        -h|--help)
            grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) echo "unknown flag: $1" >&2; exit 2;;
    esac
done

log() { printf '[uninstall] %s\n' "$*"; }

# ---- Step 1: settings.json ----
if [ -f "$SETTINGS_FP" ]; then
    if ! command -v python3 >/dev/null 2>&1; then
        echo "[uninstall] python3 required to strip hooks safely." >&2
        exit 1
    fi
    TS="$(date +%Y%m%d-%H%M%S)"
    BAK="$SETTINGS_FP.bak-$TS"
    if [ $DRY_RUN -eq 1 ]; then
        log "[dry-run] backup $SETTINGS_FP -> $BAK"
    else
        cp -f "$SETTINGS_FP" "$BAK"
        log "backup created: $BAK"
    fi

    export MARKER_KEY MARKER_VALUE SETTINGS_FP DRY_RUN

    python3 - <<'PYEOF'
import json, os, sys
from collections import OrderedDict

marker_key   = os.environ['MARKER_KEY']
marker_value = os.environ['MARKER_VALUE']
fp           = os.environ['SETTINGS_FP']
dry_run      = os.environ.get('DRY_RUN') == '1'

def is_ours(grp):
    return isinstance(grp, dict) and grp.get(marker_key) == marker_value

with open(fp, 'r', encoding='utf-8') as f:
    raw = f.read().strip()
if not raw:
    print("[uninstall] settings.json is empty, nothing to do")
    sys.exit(0)

try:
    settings = json.loads(raw, object_pairs_hook=OrderedDict)
except Exception as e:
    sys.stderr.write("[uninstall] settings.json is not valid JSON: %s\n" % e)
    sys.exit(1)

hooks = settings.get('hooks')
if not isinstance(hooks, dict):
    print("[uninstall] no 'hooks' key, nothing to strip")
    sys.exit(0)

new_hooks = OrderedDict()
removed = 0
for ev, groups in hooks.items():
    kept = []
    for grp in (groups or []):
        if is_ours(grp):
            removed += 1
            continue
        kept.append(grp)
    if kept:
        new_hooks[ev] = kept

if new_hooks:
    settings['hooks'] = new_hooks
else:
    settings.pop('hooks', None)

out = json.dumps(settings, indent=2, ensure_ascii=False)
if dry_run:
    print("[uninstall] [dry-run] would remove %d entries; new settings.json:" % removed)
    print(out)
else:
    with open(fp, 'w', encoding='utf-8') as f:
        f.write(out + "\n")
    print("[uninstall] removed %d coding-pet-managed hook entries" % removed)
PYEOF
else
    log "settings.json not found, skip"
fi

# ---- Step 2: delete hook script files ----
if [ $KEEP_SCRIPTS -eq 1 ]; then
    log "keep-scripts flag set, hook script files retained"
else
    for fp in "$SCRIPT_DST" "$ENV_FILE"; do
        if [ -f "$fp" ]; then
            if [ $DRY_RUN -eq 1 ]; then log "[dry-run] rm $fp"
            else rm -f "$fp"; log "deleted: $fp"; fi
        fi
    done
    if [ -d "$HOOK_DIR" ] && [ -z "$(ls -A "$HOOK_DIR" 2>/dev/null)" ]; then
        if [ $DRY_RUN -eq 1 ]; then log "[dry-run] rmdir $HOOK_DIR"
        else rmdir "$HOOK_DIR"; log "removed empty dir: $HOOK_DIR"; fi
    fi
fi

log "done. Restart CodeBuddy IDE to fully deactivate the hooks."

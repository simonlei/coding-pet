#!/usr/bin/env bash
# install.sh -- Install CodeBuddy IDE hooks for coding-pet dashboard (Unix).
#
# What it does:
#   1. Copy coding-pet-hook.sh to $HOME/.codebuddy/hooks/ and chmod +x
#   2. Backup existing settings.json to settings.json.bak-<ts> (if any)
#   3. Merge 7 hook entries (all tagged "__managed_by":"coding-pet") into
#      $HOME/.codebuddy/settings.json, idempotently.
#
# Usage:
#   ./install.sh                       # default
#   ./install.sh --hook-url URL        # custom agent URL, saved to agent.env
#   ./install.sh --dry-run             # print only, no writes
#
# Requires python3 (used for safe JSON merge; almost all modern distros ship it).

set -eu

MARKER_KEY='__managed_by'
MARKER_VALUE='coding-pet'
CB_DIR="$HOME/.codebuddy"
HOOK_DIR="$CB_DIR/hooks"
SETTINGS_FP="$CB_DIR/settings.json"
SCRIPT_DST="$HOOK_DIR/coding-pet-hook.sh"
ENV_FILE="$HOOK_DIR/agent.env"

SCRIPT_SRC="$(cd "$(dirname "$0")" && pwd)/coding-pet-hook.sh"

HOOK_URL=''
DRY_RUN=0
while [ $# -gt 0 ]; do
    case "$1" in
        --hook-url) HOOK_URL="${2:-}"; shift 2;;
        --dry-run)  DRY_RUN=1; shift;;
        -h|--help)
            grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) echo "unknown flag: $1" >&2; exit 2;;
    esac
done

log() { printf '[install] %s\n' "$*"; }

if ! command -v python3 >/dev/null 2>&1; then
    echo "[install] python3 is required for safe JSON merge but not found." >&2
    echo "[install] Please install python3 (e.g. brew install python) and retry." >&2
    exit 1
fi

if [ ! -f "$SCRIPT_SRC" ]; then
    echo "[install] source hook script not found: $SCRIPT_SRC" >&2
    exit 1
fi

# ---- Step 1: prepare directory & copy hook script ----
if [ $DRY_RUN -eq 1 ]; then
    log "[dry-run] mkdir -p $HOOK_DIR"
    log "[dry-run] cp $SCRIPT_SRC -> $SCRIPT_DST && chmod +x"
else
    mkdir -p "$HOOK_DIR"
    cp -f "$SCRIPT_SRC" "$SCRIPT_DST"
    chmod +x "$SCRIPT_DST"
    log "hook script installed: $SCRIPT_DST"
fi

# ---- Step 2: optional agent.env for custom URL ----
if [ -n "$HOOK_URL" ]; then
    if [ $DRY_RUN -eq 1 ]; then
        log "[dry-run] write agent.env with URL=$HOOK_URL"
    else
        printf 'DASHBOARD_IDE_HOOK_URL=%s\n' "$HOOK_URL" > "$ENV_FILE"
        log "custom hook URL saved: $ENV_FILE"
    fi
fi

# ---- Step 3: backup settings.json ----
if [ -f "$SETTINGS_FP" ]; then
    TS="$(date +%Y%m%d-%H%M%S)"
    BAK="$SETTINGS_FP.bak-$TS"
    if [ $DRY_RUN -eq 1 ]; then
        log "[dry-run] backup $SETTINGS_FP -> $BAK"
    else
        cp -f "$SETTINGS_FP" "$BAK"
        log "backup created: $BAK"
    fi
fi

# ---- Step 4+5: merge hooks via python3 ----
COMMAND='$HOME/.codebuddy/hooks/coding-pet-hook.sh'
export MARKER_KEY MARKER_VALUE SETTINGS_FP COMMAND DRY_RUN

python3 - <<'PYEOF'
import json, os, sys
from collections import OrderedDict

marker_key   = os.environ['MARKER_KEY']
marker_value = os.environ['MARKER_VALUE']
fp           = os.environ['SETTINGS_FP']
command      = os.environ['COMMAND']
dry_run      = os.environ.get('DRY_RUN') == '1'

def is_ours(grp):
    return isinstance(grp, dict) and grp.get(marker_key) == marker_value

def new_group(matcher):
    g = OrderedDict()
    if matcher:
        g['matcher'] = matcher
    g['hooks'] = [OrderedDict([('type','command'),('command',command),('timeout',5)])]
    g[marker_key] = marker_value
    return g

fresh = OrderedDict([
    ('SessionStart',     [new_group('startup')]),
    ('SessionEnd',       [new_group('other')]),
    ('UserPromptSubmit', [new_group('')]),
    ('PreToolUse',       [new_group('*')]),
    ('PostToolUse',      [new_group('*')]),
    ('Stop',             [new_group('')]),
    ('PreCompact',       [new_group('auto')]),
])

settings = OrderedDict()
if os.path.exists(fp):
    with open(fp, 'r', encoding='utf-8') as f:
        raw = f.read().strip()
    if raw:
        try:
            settings = json.loads(raw, object_pairs_hook=OrderedDict)
        except Exception as e:
            sys.stderr.write("[install] settings.json is not valid JSON: %s\n" % e)
            sys.exit(1)

existing = settings.get('hooks') or OrderedDict()
merged = OrderedDict()

# Union of event names, existing first to preserve order.
event_names = list(existing.keys()) + [k for k in fresh.keys() if k not in existing]
for ev in event_names:
    kept = []
    for grp in (existing.get(ev) or []):
        if is_ours(grp):
            continue  # drop coding-pet-marked
        kept.append(grp)
    if ev in fresh:
        kept.extend(fresh[ev])
    if kept:
        merged[ev] = kept

settings['hooks'] = merged
out = json.dumps(settings, indent=2, ensure_ascii=False)

if dry_run:
    print("[install] [dry-run] would write settings.json:")
    print(out)
else:
    with open(fp, 'w', encoding='utf-8') as f:
        f.write(out + "\n")
    print("[install] settings.json updated: %s" % fp)
PYEOF

log "done. Please restart CodeBuddy IDE to activate the hooks."

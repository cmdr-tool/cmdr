#!/bin/bash
# cmdr: First-run setup. Prompts for config values and writes ~/.cmdr/cmdr.env.
# Re-running re-prompts with existing values as defaults.

set -e

CONFIG_DIR="$HOME/.cmdr"
CONFIG_FILE="$CONFIG_DIR/cmdr.env"
mkdir -p "$CONFIG_DIR"

# Load existing values as defaults (if the file exists)
if [ -f "$CONFIG_FILE" ]; then
    # shellcheck disable=SC1090
    source "$CONFIG_FILE"
fi

# Built-in defaults (first run)
: "${CMDR_LABEL:=com.cmdr-tool.cmdr}"
: "${CMDR_CODE_DIR:=$HOME/Code}"
: "${CMDR_SUMMARIZER:=apple}"
: "${CMDR_OLLAMA_URL:=http://localhost:11434}"
: "${CMDR_OLLAMA_MODEL:=gemma4:e4b}"
: "${CMDR_MULTIPLEXER:=tmux}"
: "${CMDR_TERMINAL_APP:=Ghostty}"
: "${CMDR_EDITOR:=nvim}"

echo "cmdr: setup"
echo "==========="
echo "Values are written to $CONFIG_FILE."
echo "Press Enter to accept the [default] shown."
echo ""

prompt() {
    local var="$1" desc="$2" current="${!1}"
    printf "%s [%s]: " "$desc" "$current"
    read -r value
    printf -v "$var" "%s" "${value:-$current}"
}

prompt CMDR_LABEL        "launchd label"
prompt CMDR_CODE_DIR     "code directory (where your git repos live)"
prompt CMDR_MULTIPLEXER  "terminal multiplexer (tmux or cmux)"
prompt CMDR_TERMINAL_APP "terminal app (Ghostty, WezTerm, cmux, etc.)"
prompt CMDR_EDITOR       "editor command (nvim, vim, code, zed, etc.)"
prompt CMDR_SUMMARIZER   "title summarizer (apple or ollama)"

# Only prompt for Ollama settings if using Ollama
if [ "$CMDR_SUMMARIZER" = "ollama" ]; then
    prompt CMDR_OLLAMA_URL   "Ollama server URL"
    prompt CMDR_OLLAMA_MODEL "Ollama model"
fi

# Expand ~ in the code dir for storage
CMDR_CODE_DIR="${CMDR_CODE_DIR/#\~/$HOME}"

cat > "$CONFIG_FILE" <<EOF
# cmdr configuration — written by scripts/setup.sh
# Edit this file directly or re-run 'bash scripts/setup.sh' to update.
CMDR_LABEL=$CMDR_LABEL
CMDR_CODE_DIR=$CMDR_CODE_DIR
CMDR_SUMMARIZER=$CMDR_SUMMARIZER
CMDR_OLLAMA_URL=$CMDR_OLLAMA_URL
CMDR_OLLAMA_MODEL=$CMDR_OLLAMA_MODEL
CMDR_MULTIPLEXER=$CMDR_MULTIPLEXER
CMDR_TERMINAL_APP=$CMDR_TERMINAL_APP
CMDR_EDITOR=$CMDR_EDITOR
EOF

echo ""
echo "cmdr: wrote $CONFIG_FILE"

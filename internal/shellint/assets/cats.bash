# cats shell integration — OSC 133 semantic prompts + the command line.
# managed by cats — CATS_INTEGRATION_ID=shell CATS_INTEGRATION_VERSION=2
#
# A terminal receives an undifferentiated byte stream: it cannot see where one
# command ends and the next begins, because "the prompt" is just more output.
# These sequences say so explicitly, and are what the cats command ledger is
# built on. They are the standard OSC 133 marks, so any terminal that
# understands them (cats, kitty, WezTerm, iTerm2, VS Code) benefits, and any
# terminal that does not skips them as unknown OSC strings.
#
#   133;A  prompt starts      133;C  the command is running
#   133;B  the user's input   133;D;N  it finished with status N
#   633;E  the command line itself (VS Code's extension; OSC 133 has no field
#          for it, and it is the field a history exists for)
#
# bash is the awkward one of the three shells: it has no preexec hook, so the
# DEBUG trap stands in — and a DEBUG trap fires before EVERY simple command,
# including each one inside PROMPT_COMMAND. Two rules keep that honest, and both
# were written after watching a ledger fill with the wrong things:
#
#   * A FLAG, not a disarm. The trap reports the first command of a prompt and
#     then stands down until the next one, so "cd /tmp; ls" is one line rather
#     than two records that disagree about the directory. (Removing the trap
#     from inside its own handler is the tempting version and does not hold: it
#     is re-armed by the time the second command in a list runs, which is
#     exactly how that pair of records was first observed.)
#   * __cats_precmd is what clears the flag, and it goes LAST in PROMPT_COMMAND
#     so anything else there has already run — a prompt framework's own work
#     never lands in the history. The trap also ignores our own functions by
#     name, which is what keeps __cats_precmd out of the ledger's first row.

# Nothing to do without an interactive shell — these bytes written into a pipe
# would corrupt whatever is reading it.
case "$-" in *i*) ;; *) return 0 ;; esac

# Cats tool setup: the plugin bin dir on PATH, then whatever `catctl
# shellinit` emits (the same PATH guard again, plus a source line per plugin
# shell hook). The PATH line here is a bootstrap duplicate on purpose — catctl
# itself may live only in ~/.cats/bin — and both sides guard against the entry
# already being present, so evaluating twice adds nothing twice.
case ":$PATH:" in *":$HOME/.cats/bin:"*) ;; *) export PATH="$HOME/.cats/bin:$PATH" ;; esac
command -v catctl >/dev/null 2>&1 && eval "$(catctl shellinit bash 2>/dev/null)"

__cats_osc() { printf '\033]%s\033\\' "$1"; }

# __cats_escape makes a command line safe inside an OSC string: ";" would start
# a new parameter, and a newline or an ESC would end the sequence outright.
__cats_escape() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//;/\\x3b}
  s=${s//$'\n'/\\x0a}
  s=${s//$'\033'/\\x1b}
  printf '%s' "$s"
}

# __cats_line is what the user actually typed, read back out of history rather
# than taken from $BASH_COMMAND — which holds only the first SIMPLE command, so
# "make && ./run" would be recorded as "make". A history that quietly drops the
# second half of every line is worse than no history.
__cats_line() {
  local line
  # "  512  make && ./run" — a leading pad, the history number (possibly with a
  # "*" marking an edited entry), then the line. Stripped with parameter
  # expansion rather than sed so this costs no fork per prompt.
  line=$(HISTTIMEFORMAT= builtin history 1 2>/dev/null)
  line=${line#"${line%%[![:space:]]*}"}   # leading blanks
  line=${line#*[[:space:]]}               # the number
  line=${line#"${line%%[![:space:]]*}"}   # blanks after it
  if [ -z "$line" ]; then
    printf '%s' "$BASH_COMMAND"           # history off, or empty
  else
    printf '%s' "$line"
  fi
}

__cats_running=""

__cats_debug_trap() {
  [ -n "$COMP_LINE" ] && return          # tab completion, not a command
  [ -n "$__cats_running" ] && return      # already reported this prompt's line
  case "$BASH_COMMAND" in __cats_*) return ;; esac  # our own prompt hook
  __cats_running=1
  __cats_osc "633;E;$(__cats_escape "$(__cats_line)")"
  __cats_osc "133;C"
}

__cats_precmd() {
  local st=$?
  if [ -n "$__cats_running" ]; then
    __cats_osc "133;D;$st"
    __cats_running=""
  fi
  __cats_osc "133;A"
  # OSC 7 is the cwd, which the ledger records per command. cats probes for it
  # too, but a shell that reports its own is authoritative — it can name a
  # directory no process probe can see.
  __cats_osc "7;file://$HOSTNAME$PWD"
  trap '__cats_debug_trap' DEBUG          # armed last: see the header
}

# The first prompt is what arms the trap, so nothing this file does while being
# sourced can end up in the history.

# 133;B belongs between the prompt and the typing, which in bash means the end
# of PS1. \[...\] tells readline the bytes take no columns, so the line editor's
# idea of the cursor position stays right.
case "$PS1" in
  *133\;B*) ;;
  *) PS1=${PS1}'\[\033]133;B\033\\\]' ;;
esac

case "$PROMPT_COMMAND" in
  *__cats_precmd*) ;;
  "") PROMPT_COMMAND="__cats_precmd" ;;
  *)  PROMPT_COMMAND="$PROMPT_COMMAND;__cats_precmd" ;;
esac

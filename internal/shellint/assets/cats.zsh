# cats shell integration — OSC 133 semantic prompts + the command line.
# managed by cats — CATS_INTEGRATION_ID=shell CATS_INTEGRATION_VERSION=2
#
# See cats.bash for what the marks mean. zsh needs none of bash's DEBUG-trap
# bookkeeping: preexec and precmd are real hooks that fire exactly once each.

[[ -o interactive ]] || return 0

# Cats tool setup: the plugin bin dir on PATH, then whatever `catctl
# shellinit` emits (the same PATH guard again, plus a source line per plugin
# shell hook). The PATH line here is a bootstrap duplicate on purpose — catctl
# itself may live only in ~/.cats/bin — and both sides guard against the entry
# already being present, so evaluating twice adds nothing twice.
case ":$PATH:" in *":$HOME/.cats/bin:"*) ;; *) export PATH="$HOME/.cats/bin:$PATH" ;; esac
command -v catctl >/dev/null 2>&1 && eval "$(catctl shellinit zsh 2>/dev/null)"

__cats_osc() { printf '\033]%s\033\\' "$1" }

__cats_escape() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//;/\\x3b}
  s=${s//$'\n'/\\x0a}
  s=${s//$'\033'/\\x1b}
  printf '%s' "$s"
}

__cats_running=""

__cats_preexec() {
  __cats_running=1
  __cats_osc "633;E;$(__cats_escape "$1")"
  __cats_osc "133;C"
}

__cats_precmd() {
  local st=$?
  if [[ -n $__cats_running ]]; then
    __cats_osc "133;D;$st"
    __cats_running=""
  fi
  __cats_osc "133;A"
  __cats_osc "7;file://${HOST}${PWD}"
}

autoload -Uz add-zsh-hook
add-zsh-hook preexec __cats_preexec
add-zsh-hook precmd __cats_precmd

# 133;B marks where the typing starts, i.e. the end of the prompt. %{...%} tells
# zsh the sequence takes no columns, so it does not corrupt the line editor's
# idea of the cursor position.
PS1="${PS1}%{$(printf '\033]133;B\033\\')%}"

# cats shell integration — OSC 133 semantic prompts + the command line.
# managed by cats — CATS_INTEGRATION_ID=shell CATS_INTEGRATION_VERSION=2
#
# See cats.bash for what the marks mean. fish has named events for all three
# moments, so this is the shortest of the three.

status is-interactive; or exit 0

# Cats tool setup: the plugin bin dir on PATH, then whatever `catctl
# shellinit` emits (the same PATH guard again, plus a source line per plugin
# shell hook). The PATH line here is a bootstrap duplicate on purpose — catctl
# itself may live only in ~/.cats/bin — and both sides guard against the entry
# already being present, so evaluating twice adds nothing twice.
if not contains -- "$HOME/.cats/bin" $PATH
    set -gx PATH "$HOME/.cats/bin" $PATH
end
command -q catctl; and catctl shellinit fish 2>/dev/null | source

function __cats_osc
    printf '\033]%s\033\\' $argv[1]
end

function __cats_escape
    string replace -a '\\' '\\\\' -- $argv[1] |
        string replace -a ';' '\\x3b' |
        string replace -a \n '\\x0a' |
        string replace -a \e '\\x1b'
end

function __cats_preexec --on-event fish_preexec
    set -g __cats_running 1
    __cats_osc "633;E;"(__cats_escape "$argv[1]")
    __cats_osc "133;C"
end

function __cats_postexec --on-event fish_postexec
    __cats_osc "133;D;$status"
    set -e __cats_running
end

function __cats_prompt --on-event fish_prompt
    __cats_osc "133;A"
    __cats_osc "7;file://"(hostname)"$PWD"
end

# 133;B goes at the very end of the prompt. fish has no PS1 to append to, so the
# mark rides fish_prompt's own output by wrapping whatever the user's prompt
# function produces.
functions -q __cats_orig_fish_prompt; or functions -c fish_prompt __cats_orig_fish_prompt
function fish_prompt
    __cats_orig_fish_prompt
    printf '\033]133;B\033\\'
end

#!/bin/sh
# stamp.sh prints the -ldflags -X values that link the current git commit's
# identity into the buildbadge package:
#
#   go build -ldflags "$(sh path/to/buildbadge/stamp.sh)" ./cmd/yourapp
#
# The one argument is buildbadge's import path, defaulting to where it lives in
# cats. A project that copied the package into its own tree passes its path:
#
#   go build -ldflags "$(sh internal/buildbadge/stamp.sh example.com/app/internal/buildbadge)" .
#
# Why each value is shaped the way it is (the Go side decodes them the same way):
#   hash        short sha — safe as-is
#   subjectB64  the commit subject, base64: it may hold spaces, quotes and '$',
#               none of which survive an -ldflags string; base64's alphabet does
#   when        committer date, strict ISO 8601 (%cI) — no spaces or quotes
#
# Outside a git checkout every value is empty, which the Go side reads as
# "not stamped" and falls back to the toolchain's own VCS info.
pkg=${1:-github.com/rohanthewiz/cats/buildbadge}
hash=$(git rev-parse --short HEAD 2>/dev/null)
subj=$(git log -1 --pretty=%s 2>/dev/null | base64 | tr -d '\n')
when=$(git log -1 --pretty=%cI 2>/dev/null)
printf '%s' "-X $pkg.hash=$hash -X $pkg.subjectB64=$subj -X $pkg.when=$when"

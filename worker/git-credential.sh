#!/bin/sh
# Hands git the step's token, which the worker writes to the file
# $FLOW_GIT_CREDENTIALS names for the step; see git_credentials.go. Without
# one, git goes on without credentials and public repositories still clone.
[ "$1" = get ] || exit 0
[ -n "$FLOW_GIT_CREDENTIALS" ] && [ -r "$FLOW_GIT_CREDENTIALS" ] || exit 0
cat "$FLOW_GIT_CREDENTIALS"

#!/bin/sh
# Runs GitHub's gh with the step's git token, read from the file
# $FLOW_GIT_CREDENTIALS names (see git_credentials.go) as gh runs, so the
# token never sits in the step's environment for every process it starts.
# The token reaches only the run's repositories; without one, or with
# GH_TOKEN already set, gh runs as it would.
if [ -z "$GH_TOKEN" ] && [ -n "$FLOW_GIT_CREDENTIALS" ] && [ -r "$FLOW_GIT_CREDENTIALS" ]; then
	GH_TOKEN=$(sed -n 's/^password=//p' "$FLOW_GIT_CREDENTIALS")
	export GH_TOKEN
fi
exec /usr/bin/gh "$@"

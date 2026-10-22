#!/bin/sh
# Starts the worker as flow, never root, so neither it nor the agents and
# scripts it runs can do what root can. Started as root, it first gives flow
# whatever in the volumes isn't flow's, then drops to flow: an image from
# before flow left them root's, and a worker that ran under SANDBOX=podman or
# docker, as root, leaves root's the directories it made itself, such as the
# runs' homes and the shared repositories. Only what isn't flow's is changed,
# so a start with nothing to change only reads the volumes.
#
# With SANDBOX=podman or docker, steps run in containers of their own, and
# the worker stays root in its container, which with a rootless engine is
# the engine's user outside it and no more: it needs the engine's socket,
# and hands what it makes for a step to the steps' user. See
# sandbox_engine.go. Under Kubernetes the pod runs it as the steps' user.
set -e
if [ "$(id -u)" = 0 ] && { [ -z "$SANDBOX" ] || [ "$SANDBOX" = process ]; }; then
	for dir in /data/files /data/workspaces /data/steps; do
		mkdir -p "$dir"
		find "$dir" \( ! -user 1000 -o ! -group 1000 \) -exec chown -h flow:flow {} +
	done
	exec su-exec flow "$@"
fi
exec "$@"

#!/bin/sh
# Starts the worker as flow, never root, so neither it nor the agents and
# scripts it runs can do what root can. Started as root, it first gives flow
# the volumes, which an image from before flow left root's (only when the
# top is root's, so a large workspace isn't walked on every start), then
# drops to flow.
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
		[ "$(stat -c %u "$dir")" = 1000 ] || chown -R flow:flow "$dir"
	done
	exec su-exec flow "$@"
fi
exec "$@"

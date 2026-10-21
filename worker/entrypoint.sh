#!/bin/sh
# Starts the worker as flow, never root, so neither it nor the agents and
# scripts it runs can do what root can. Started as root, it first gives flow
# the volumes, which an image from before flow left root's (only when the
# top is root's, so a large workspace isn't walked on every start), then
# drops to flow.
set -e
if [ "$(id -u)" = 0 ]; then
	for dir in /data/files /data/workspaces; do
		mkdir -p "$dir"
		[ "$(stat -c %u "$dir")" = 1000 ] || chown -R flow:flow "$dir"
	done
	exec su-exec flow "$@"
fi
exec "$@"

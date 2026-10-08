#!/bin/sh
# Hands git the token in $GIT_TOKEN for https remotes, so cloning private
# repositories works without storing it anywhere. Without a token, git goes
# on without credentials and public repositories still clone.
[ "$1" = get ] || exit 0
[ -n "$GIT_TOKEN" ] || exit 0
echo "username=${GIT_USERNAME:-x-access-token}"
echo "password=$GIT_TOKEN"

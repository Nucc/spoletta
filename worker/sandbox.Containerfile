# What a step's container runs when steps run in containers of their own
# (SANDBOX=podman; see sandbox_podman.go): pi, with the extensions the
# worker gives it, and bash, git, curl and jq for agents' tools and
# scripts. Nothing of the worker's: it stays in the worker's image.
FROM docker.io/library/node:22-alpine

RUN apk add --no-cache bash curl git jq
RUN npm install -g --ignore-scripts @earendil-works/pi-coding-agent@1.0.4

COPY worker/submit-output.ts /opt/flow/submit-output.ts
COPY worker/mcp-servers.ts /opt/flow/mcp-servers.ts
COPY --chmod=755 worker/git-credential.sh /opt/flow/git-credential.sh
# Agents commit to their run's branch as this, unless GIT_AUTHOR_NAME and
# the like say otherwise. The step's environment names its git token.
RUN git config --system user.name "Flow agent" \
 && git config --system user.email "flow-agent@localhost"

# Steps run as flow, uid 1000, in place of the image's node user, with a
# read-only root and their home on a tmpfs.
RUN deluser --remove-home node && adduser -D -u 1000 -h /tmp flow
USER 1000:1000

# What a step's container runs when steps run in containers of their own
# (SANDBOX=podman; see sandbox_podman.go): pi, with the extensions the
# worker gives it, and what a coding agent reaches for: bash, git, curl
# and jq, ripgrep and fd to search, GNU's coreutils, grep, sed and awk in
# place of busybox's, diff and patch, archivers, make, and Python with pip
# (installing into the step's home, as the root is read-only). Nothing
# of the worker's: it stays in the worker's image. Node
# names its patch release, as in the worker's image.
FROM docker.io/library/node:22.23.4-alpine

RUN apk add --no-cache bash curl git jq \
    ripgrep fd tree file less \
    coreutils findutils grep sed gawk diffutils patch \
    tar gzip xz zip unzip make \
    python3 py3-pip
# Agents call python as often as python3. Alpine marks its Python as the
# system's, so pip installs nothing without being told; a step's packages
# go to its home (--user), never the image.
RUN ln -s /usr/bin/python3 /usr/local/bin/python
ENV PIP_USER=1 PIP_BREAK_SYSTEM_PACKAGES=1
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

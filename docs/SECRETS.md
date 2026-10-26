# Sealed secrets and rotating SECRET_KEY

The webapp keeps three kinds of secret for others in MySQL: the model API
keys admins save in the settings, the OAuth tokens of each person's MCP
connections, and the secrets of the credentials admins keep for scripts and
agents (see [webapp/credentials.go](../webapp/credentials.go)). With `SECRET_KEY` set, each is sealed with AES-256-GCM under a
key derived from it, bound to the row it is kept in, so a dump or a backup
of the database doesn't hand them out. How it works is described at the top
of [webapp/secrets.go](../webapp/secrets.go).

## Setting it up

Make a key of at least 32 characters, and keep it where you keep the
database's backups: without it, the secrets in them can't be opened.

```sh
openssl rand -base64 32
```

Give it to every webapp as `SECRET_KEY`, or in a file whose path is in
`SECRET_KEY_FILE`, as with secret stores that mount secrets as files, such
as the Vault Agent Injector or the Secrets Store CSI driver; the same goes
for `SECRET_KEY_SECONDARY` and `SECRET_KEY_SECONDARY_FILE`. On starting, a webapp seals the
secrets kept unsealed before, and checks it opens every sealed one; started
with another key, or none, it stops with an error instead of failing each
agent step.

## Rotating

`SECRET_KEY_SECONDARY` is the key coming in. A webapp that has it opens
secrets sealed with either key and seals new ones with the secondary, but
leaves those sealed with the primary as they are, since webapps still
without the secondary go on reading them. `webapp seal-secrets` seals them
again with the secondary and exits: 0 once every secret is sealed with it,
1 if some changed while it ran (run it again), and it stops with an error
naming a secret neither key opens.

The steps are the same wherever the webapps' environment comes from; how
to set a key in a few common places follows them. Setting a key to the
empty string is the same as leaving it out.

1. **Add the secondary key**: make one with `openssl rand -base64 32`, set it
   as `SECRET_KEY_SECONDARY`, and restart the webapps so every one has it.

   Secrets stay sealed with the primary meanwhile, so the webapps not yet
   restarted keep working. What a restarted webapp saves while the restart
   goes on, such as a new model key or a refreshed MCP token, is sealed
   with the secondary, which those can't open: an agent step there using it
   fails until they restart too. A single replica, or in Kubernetes
   `strategy: Recreate`, avoids that window. Let every webapp restart before
   step 2, as one still without the secondary seals with the primary.

2. **Seal everything with the secondary** by running `webapp seal-secrets`
   once, with the webapps' image and environment (database and both keys).
   It logs `all N secrets are sealed with SECRET_KEY_SECONDARY`.

3. **Make the secondary the primary**: set `SECRET_KEY` to the new key and
   `SECRET_KEY_SECONDARY` to nothing, and restart the webapps again.

   Webapps not yet restarted seal with the new key too, so nothing is
   sealed with the old one any more. Should a secret still be, the new
   webapps stop on starting, and in Kubernetes the rollout stalls with the
   old ones serving; put the old key back as `SECRET_KEY_SECONDARY` and run
   step 2 again.

The old key opens backups taken before the rotation, so keep it as long as
those.

### Kubernetes

The webapp's Deployment takes `SECRET_KEY` and `SECRET_KEY_SECONDARY` from
a Secret, here named `spoletta`, which you change in step 1 and 3 (below),
then restart it:

```sh
kubectl rollout restart deployment/spoletta-webapp
kubectl rollout status deployment/spoletta-webapp
```

Step 2 is a Job:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: spoletta-seal-secrets
spec:
  backoffLimit: 3
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: seal-secrets
          image: <the webapp's image>
          args: ["webapp", "seal-secrets"]
          envFrom:
            - secretRef:
                name: spoletta
          # plus DB_HOST, DB_NAME, DB_USER, DB_PASSWORD as the Deployment has them
```

```sh
kubectl apply -f seal-secrets-job.yaml
kubectl wait --for=condition=complete job/spoletta-seal-secrets --timeout=5m
kubectl logs job/spoletta-seal-secrets
```

It exits 1 if some secret changed while it ran, and the Job runs it again.

**A Secret kept by hand** is changed with `kubectl patch`:

```sh
NEW=$(openssl rand -base64 32)
# step 1
kubectl patch secret spoletta --type merge -p "{\"stringData\":{\"SECRET_KEY_SECONDARY\":\"$NEW\"}}"
# step 3
kubectl patch secret spoletta --type merge -p "{\"stringData\":{\"SECRET_KEY\":\"$NEW\",\"SECRET_KEY_SECONDARY\":\"\"}}"
```

**A Secret a secrets manager keeps in sync**, such as the Vault Secrets
Operator or External Secrets from Vault, AWS Secrets Manager or another
store, is changed in that store instead, as the manager would undo a patch.
Wait for the Secret to sync before restarting: check it with
`kubectl get secret spoletta -o jsonpath='{.data.SECRET_KEY_SECONDARY}' | base64 -d`,
or have the manager sync at once (External Secrets:
`kubectl annotate externalsecret spoletta force-sync=$(date +%s) --overwrite`).
The Vault Secrets Operator can restart the Deployment itself once the Secret
changes, with `rolloutRestartTargets`. With Vault's KV store, for example:

```sh
# step 1
vault kv patch secret/spoletta SECRET_KEY_SECONDARY="$NEW"
# step 3
vault kv patch secret/spoletta SECRET_KEY="$NEW" SECRET_KEY_SECONDARY=""
```

**Keys mounted as files**, as the Vault Agent Injector or the Secrets
Store CSI driver do, are changed in the store too, and the webapps given
`SECRET_KEY_FILE` and `SECRET_KEY_SECONDARY_FILE`. The webapp reads them
only on starting, so restart it once the files changed, as above; an empty
file counts as no key, which is how the secondary goes in step 3. The Job
needs the same files mounted.

### Compose

Set the keys in `.env`, restart with `podman compose up -d webapp`, and run
step 2 with `podman compose run --rm webapp webapp seal-secrets`.

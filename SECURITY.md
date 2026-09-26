# Security

## Reporting

Report a vulnerability through [GitHub private vulnerability reporting](https://github.com/moezdil/siltide/security/advisories/new). Please do not open a public issue for it. You should hear back within 7 days.

## What siltide exposes

- `--listen` serves the snapshot, events, history, and Prometheus metrics. Off loopback it refuses to start without TLS and a token, unless `insecure: true` is set.
- Tokens are compared as SHA-256 digests in constant time; `--gen-token` prints a token and its digest so the config holds only the digest.
- `/api/snapshot` strips process command lines. `--once --json` and `--record` do not; treat those files as you would a process listing.
- The Kubernetes integration reads pods and, on request, pod descriptions and logs with the credentials it finds (pod-resources socket, in-cluster service account, or kubeconfig). Give the DaemonSet only the RBAC in `deploy/kubernetes/daemonset.yaml`.
- The SSH provider runs vendor CLIs on remote hosts through the system `ssh` client with the key you configure. It runs nothing else.
- Webhook, Slack, and Alertmanager outputs send alert text (device name, node, rule) to the URLs you configure.

## Supported versions

The latest release and the `main` pre-releases receive fixes.

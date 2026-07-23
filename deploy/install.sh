#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID} -ne 0 ]]; then
  echo "run this installer as root" >&2
  exit 1
fi

BINARY=${1:-./centralcloud-agent}
if [[ ! -x ${BINARY} ]]; then
  echo "agent binary not found or not executable: ${BINARY}" >&2
  exit 1
fi

getent group docker >/dev/null
id centralcloud-agent >/dev/null 2>&1 || useradd --system --uid 10001 --home /var/lib/centralcloud-agent --shell /usr/sbin/nologin --groups docker centralcloud-agent
install -D -m 0755 "${BINARY}" /usr/local/bin/centralcloud-agent
install -d -o centralcloud-agent -g centralcloud-agent -m 0700 /var/lib/centralcloud-agent /var/lib/centralcloud-agent/backups /var/lib/centralcloud-agent/panels /run/centralcloud-agent
install -d -o root -g centralcloud-agent -m 0750 /etc/centralcloud-agent /etc/centralcloud-agent/tls /etc/centralcloud-agent/secrets
install -m 0644 deploy/systemd/centralcloud-agent.service /etc/systemd/system/centralcloud-agent.service
if [[ ! -f /etc/centralcloud-agent/config.yaml ]]; then
  install -m 0640 -o root -g centralcloud-agent deploy/examples/config.yaml /etc/centralcloud-agent/config.yaml
fi
systemctl daemon-reload
echo "Install config.yaml and root-protected secret files (including api_token.sha256), validate them, then run: systemctl enable --now centralcloud-agent"

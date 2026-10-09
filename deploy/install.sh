#!/bin/bash
set -euo pipefail
# Run as root from a copied checkout containing bin/contests.
# First installation requires CONTEST_PASSWORD; subsequent runs preserve the database.
cd "$(dirname "$0")/.."
if [[ $(id -u) != 0 ]]; then echo 'Run as root' >&2; exit 1; fi
test -x bin/contests
test -x /usr/local/bin/isolate
if [[ ! -f /var/lib/contests/contests.db ]]; then
    : "${CONTEST_PASSWORD:?Provide initial admin password in CONTEST_PASSWORD}"
fi
# The sandbox must be idle before the self-test; active jobs recover from SQLite.
for service in contests-worker.service contests-web.service; do
    if systemctl cat "$service" >/dev/null 2>&1; then systemctl stop "$service"; fi
done
if ! getent passwd contests >/dev/null; then useradd --system --user-group --home-dir /var/lib/contests --shell /usr/sbin/nologin contests; fi
install -d -o contests -g contests -m 0750 /var/lib/contests
install -d -m 0755 /opt/contests /etc/systemd/system/isolate.slice.d /var/local/lib/isolate
install -m 0755 bin/contests /opt/contests/contests.new
mv /opt/contests/contests.new /opt/contests/contests
install -m 0644 deploy/isolate.conf /usr/local/etc/isolate
chown root:contests /usr/local/bin/isolate
chmod 4750 /usr/local/bin/isolate
install -m 0644 deploy/contests-web.service deploy/contests-worker.service deploy/var-local-lib-isolate.mount /etc/systemd/system/
install -m 0644 deploy/isolate-limits.conf /etc/systemd/system/isolate.slice.d/limits.conf
if [[ ! -f /var/lib/contests/contests.db ]]; then
    runuser -u contests --preserve-environment -- /opt/contests/contests init -db /var/lib/contests/contests.db
fi
unset CONTEST_PASSWORD
install -m 0644 deploy/nginx.conf /etc/nginx/sites-available/contests
if [[ -L /etc/nginx/sites-enabled/default ]]; then
    mv /etc/nginx/sites-enabled/default /etc/nginx/sites-available/contests-previous-default-link
fi
ln -sfn /etc/nginx/sites-available/contests /etc/nginx/sites-enabled/contests
nginx -t
systemctl daemon-reload
systemctl enable --now isolate.service var-local-lib-isolate.mount
runuser -u contests -- /opt/contests/contests sandbox-check
systemctl enable contests-web.service contests-worker.service
systemctl restart contests-web.service contests-worker.service
curl -fsS --retry 10 --retry-connrefused --retry-delay 1 --max-time 2 http://127.0.0.1:8080/healthz >/dev/null
systemctl reload nginx
systemctl is-active contests-web.service contests-worker.service nginx isolate.service

#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PI_DIR="$(dirname "$SCRIPT_DIR")"

echo "==> Installing nginx snippet"
sudo rm -f /etc/nginx/sites-enabled/train_trip /etc/nginx/sites-available/train_trip
sudo mkdir -p /etc/nginx/snippets
sudo cp "$SCRIPT_DIR/nginx/train_trip.conf" /etc/nginx/snippets/train_trip.conf
sudo nginx -t
sudo systemctl reload nginx

echo "==> Installing systemd service"
sudo cp "$SCRIPT_DIR/systemd/train_trip.service" /etc/systemd/system/train_trip.service
sudo systemctl daemon-reload
sudo systemctl enable train_trip

echo "==> Starting train_trip service"
sudo systemctl restart train_trip
sudo systemctl status train_trip --no-pager

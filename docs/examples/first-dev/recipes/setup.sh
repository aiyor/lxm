#!/bin/bash
set -euo pipefail
echo "setting up dev-station"
apt-get update -qq
apt-get install -y -qq jq ripgrep
mkdir -p /workspace
echo "dev-station ready: $(hostname)"

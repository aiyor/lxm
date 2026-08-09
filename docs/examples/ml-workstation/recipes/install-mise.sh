#!/bin/bash
set -euo pipefail

# Install mise for the primary user ($LXM_USER is exported by lxm's cloud-init).
MISE_USER="${LXM_USER:-ml}"
su - "$MISE_USER" -c 'curl -fsSL https://mise.run | sh' >/dev/null
su - "$MISE_USER" -c 'echo "export PATH=\"$HOME/.local/bin:\$PATH\"" >> "$HOME/.bashrc"'
su - "$MISE_USER" -c "echo \"mise installed: \$(mise --version)\""

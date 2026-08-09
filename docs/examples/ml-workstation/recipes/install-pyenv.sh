#!/bin/bash
set -euo pipefail

# Install pyenv for the primary user.
PYENV_USER="${LXM_USER:-ml}"
su - "$PYENV_USER" -c 'curl -fsSL https://pyenv.run | bash' >/dev/null
su - "$PYENV_USER" -c 'echo "export PYENV_ROOT=\"\$HOME/.pyenv\"" >> "$HOME/.bashrc"' 
su - "$PYENV_USER" -c 'echo "export PATH=\"\$PYENV_ROOT/bin:\$PATH\"" >> "$HOME/.bashrc"'
su - "$PYENV_USER" -c 'echo "eval \"\$(pyenv init --path)\"" >> "$HOME/.bashrc"'
su - "$PYENV_USER" -c "echo \"pyenv installed: \$(pyenv --version)\""

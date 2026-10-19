#!/usr/bin/env sh
set -eu
. "$(dirname "$0")/_common.sh"

if [ -z "${HOME:-}" ]; then
	printf '%s\n' 'HOME is not set; cannot install gwagent.' >&2
	exit 1
fi

install_dir="$HOME/.local/bin"
mkdir -p "$install_dir"
go build -o "$install_dir/gwagent$EXE" ./cmd/agent
printf 'Installed gwagent to %s/gwagent%s\n' "$install_dir" "$EXE"

case ":${PATH:-}:" in
	*":$install_dir:"*) printf '%s\n' 'The install directory is already in PATH.' ;;
	*)
		bashrc="$HOME/.bashrc"
		path_line='export PATH="$HOME/.local/bin:$PATH"'
		if ! grep -Fq "$path_line" "$bashrc" 2>/dev/null; then
			printf '\n# Go-AI-Gateway CLI\n%s\n' "$path_line" >> "$bashrc"
		fi
		printf '%s\n' 'Added the install directory to ~/.bashrc.'
		printf '%s\n' 'Run: export PATH="$HOME/.local/bin:$PATH"'
		;;
esac

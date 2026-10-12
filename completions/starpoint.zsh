# Zsh completion for Starpoint. Install under a directory on $fpath as
# _starpoint (for example ~/.zfunc/_starpoint).
_starpoint() {
  local -a commands options providers
  commands=(init sync list info explain doctor install remove transition update runs history prune recover help)
  options=(--json --provider --force --context --record --no-confirm --dry-run --version --help)
  providers=(apt brew cargo dnf gem golang npm pacman pip pnpm rustup shell system uv yarn)
  if (( CURRENT == 2 )); then
    compadd -- $commands $options
  elif [[ ${words[CURRENT-1]} == --provider || ${words[CURRENT-1]} == -p ]]; then
    compadd -- $providers
  elif [[ ${words[CURRENT]} == -* ]]; then
    compadd -- $options
  fi
}
compdef _starpoint starpoint

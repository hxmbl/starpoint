#compdef starpoint
# Zsh completion for Starpoint. Install under a directory on $fpath as
# _starpoint (for example ~/.zfunc/_starpoint).
#
# Keep in sync with internal/cli/help.go (commandFlags); a test checks it.
_starpoint() {
  local -a commands globals providers flags
  local cmd word
  commands=(init sync list ls inventory adopt info show explain doctor install add i remove uninstall rm transition move update upgrade runs history prune recover logging logs help)
  globals=(--json --provider --force --context --verbose --color --allow-destructive --allow-root --version --help)
  providers=(apt brew cargo dnf flatpak gem go nix npm pacman pip pnpm rustup snap system uv yarn)

  if [[ ${words[CURRENT-1]} == --provider || ${words[CURRENT-1]} == -p ]]; then
    compadd -- $providers
    return
  fi
  if [[ ${words[CURRENT-1]} == --context || ${words[CURRENT-1]} == --directory ]]; then
    _files -/
    return
  fi

  cmd=''
  for word in ${words[2,CURRENT-1]}; do
    if [[ $word != -* ]]; then cmd=$word; break; fi
  done
  if [[ -z $cmd ]]; then
    compadd -- $commands $globals
    return
  fi

  case $cmd in
    init) flags=(--force) ;;
    sync) flags=(--dry-run --no-confirm) ;;
    list|ls) flags=(--managed --provider) ;;
    inventory) flags=(--provider) ;;
    adopt) flags=(--all --provider --dry-run --no-confirm) ;;
    info|show) flags=() ;;
    explain) flags=(--provider) ;;
    doctor) flags=(--all) ;;
    install|add|i) flags=(--version --channel --project --provider --allow-duplicate --dry-run --no-confirm) ;;
    remove|uninstall|rm) flags=(--provider --dry-run --no-confirm --allow-empty) ;;
    logging|logs) flags=() ;;
    transition|move) flags=(--all --reverse --dry-run --no-confirm) ;;
    update|upgrade) flags=(--self --dry-run --no-confirm) ;;
    runs|history) flags=(--package --directory) ;;
    prune) flags=(--dry-run --no-confirm) ;;
    recover) flags=(--discard --no-confirm) ;;
    help) compadd -- $commands; return ;;
    *) flags=() ;;
  esac
  if [[ ${words[CURRENT]} == -* ]]; then
    compadd -- $flags --help --json
  elif [[ $cmd == install || $cmd == add || $cmd == i ]]; then
    compadd -- . $providers
  elif [[ $cmd == adopt ]]; then
    compadd -- $providers
  fi
}
# Autoloaded from $fpath (via #compdef) the file body runs as the completion
# function itself; sourced directly, it registers the function instead.
if [[ "$funcstack[1]" == "_starpoint" ]]; then
  _starpoint "$@"
else
  compdef _starpoint starpoint
fi

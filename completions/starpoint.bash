# Bash completion for Starpoint. Install by sourcing this file from
# ~/.bash_completion or copying it into /etc/bash_completion.d/.
#
# Keep in sync with internal/cli/help.go (commandFlags); a test checks it.
_starpoint() {
  local cur prev cmd i commands globals providers flags
  cur=${COMP_WORDS[COMP_CWORD]}
  prev=${COMP_WORDS[COMP_CWORD-1]}
  commands='init sync list ls inventory adopt info show explain doctor install add i remove uninstall rm transition move update upgrade runs history prune recover logging logs help'
  globals='--json --provider --force --context --verbose --color --allow-destructive --allow-root --version --help'
  providers='apt brew cargo dnf flatpak gem go nix npm pacman pip pnpm rustup snap system uv yarn'

  if [[ $prev == --provider || $prev == -p ]]; then
    COMPREPLY=( $(compgen -W "$providers" -- "$cur") )
    return
  fi
  if [[ $prev == --context || $prev == --directory ]]; then
    COMPREPLY=( $(compgen -d -- "$cur") )
    return
  fi

  cmd=''
  for (( i=1; i<COMP_CWORD; i++ )); do
    if [[ ${COMP_WORDS[i]} != -* ]]; then cmd=${COMP_WORDS[i]}; break; fi
  done
  if [[ -z $cmd ]]; then
    COMPREPLY=( $(compgen -W "$commands $globals" -- "$cur") )
    return
  fi

  case $cmd in
    init) flags='--force' ;;
    sync) flags='--dry-run --no-confirm' ;;
    list|ls) flags='--managed --provider' ;;
    inventory) flags='--provider --format --pin-versions' ;;
    adopt) flags='--all --provider --dry-run --no-confirm' ;;
    info|show) flags='' ;;
    explain) flags='--provider' ;;
    doctor) flags='--all' ;;
    install|add|i) flags='--version --channel --project --provider --allow-duplicate --dry-run --no-confirm' ;;
    remove|uninstall|rm) flags='--provider --dry-run --no-confirm --allow-empty' ;;
    logging|logs) flags='' ;;
    transition|move) flags='--all --reverse --dry-run --no-confirm' ;;
    update|upgrade) flags='--self --dry-run --no-confirm' ;;
    runs|history) flags='--package --directory' ;;
    prune) flags='--dry-run --no-confirm' ;;
    recover) flags='--discard --no-confirm' ;;
    help) COMPREPLY=( $(compgen -W "$commands" -- "$cur") ); return ;;
    *) flags='' ;;
  esac
  if [[ $cur == -* ]]; then
    COMPREPLY=( $(compgen -W "$flags --help --json" -- "$cur") )
  elif [[ $cmd == install || $cmd == add || $cmd == i ]]; then
    COMPREPLY=( $(compgen -W ". $providers" -- "$cur") )
  elif [[ $cmd == adopt ]]; then
    COMPREPLY=( $(compgen -W "$providers" -- "$cur") )
  fi
}
complete -F _starpoint starpoint

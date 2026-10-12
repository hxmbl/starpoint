# Bash completion for Starpoint. Install by sourcing this file from
# ~/.bash_completion or copying it into /etc/bash_completion.d/.
_starpoint() {
  local cur prev commands options
  cur=${COMP_WORDS[COMP_CWORD]}
  prev=${COMP_WORDS[COMP_CWORD-1]}
  commands='init sync list info explain doctor install remove transition update runs history prune recover help'
  options='--json --provider --force --context --record --no-confirm --dry-run --version --help'
  if [[ $COMP_CWORD == 1 ]]; then
    COMPREPLY=( $(compgen -W "$commands $options" -- "$cur") )
  elif [[ $prev == --provider || $prev == -p ]]; then
    COMPREPLY=( $(compgen -W 'apt brew cargo dnf gem golang npm pacman pip pnpm rustup shell system uv yarn' -- "$cur") )
  elif [[ $cur == -* ]]; then
    COMPREPLY=( $(compgen -W "$options" -- "$cur") )
  fi
}
complete -F _starpoint starpoint

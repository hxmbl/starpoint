# Fish completion for Starpoint. Install with:
#   mkdir -p ~/.config/fish/completions
#   cp completions/starpoint.fish ~/.config/fish/completions/
complete -c starpoint -f
complete -c starpoint -n '__fish_use_subcommand' -a 'init sync list info explain doctor install remove transition update runs history prune recover help'
complete -c starpoint -n '__fish_use_subcommand' -l json -d 'Machine-readable output'
complete -c starpoint -n '__fish_use_subcommand' -l provider -r -a 'apt brew cargo dnf gem golang npm pacman pip pnpm rustup shell system uv yarn'
complete -c starpoint -n '__fish_use_subcommand' -l force -d 'Replace existing configuration'
complete -c starpoint -n '__fish_use_subcommand' -l context -r -d 'Override working directory'
complete -c starpoint -n '__fish_use_subcommand' -l record -d 'Force recording execution'
complete -c starpoint -n '__fish_use_subcommand' -l no-confirm -d 'Skip confirmation'
complete -c starpoint -n '__fish_use_subcommand' -l dry-run -d 'Preview changes'
complete -c starpoint -n '__fish_use_subcommand' -l version -d 'Print version'
complete -c starpoint -n '__fish_use_subcommand' -l help -d 'Show help'

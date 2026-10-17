# Fish completion for Starpoint. Install with:
#   mkdir -p ~/.config/fish/completions
#   cp completions/starpoint.fish ~/.config/fish/completions/
#
# Keep in sync with internal/cli/help.go (commandFlags); a test checks it.
set -l commands init sync list ls info show explain doctor install add i remove uninstall rm transition move update upgrade runs history prune recover logging logs help
set -l providers apt brew cargo dnf gem go npm pacman pip pnpm rustup system uv yarn

complete -c starpoint -f
complete -c starpoint -n "not __fish_seen_subcommand_from $commands" -a "$commands"

# Global options
complete -c starpoint -l json -d 'Machine-readable output'
complete -c starpoint -l provider -r -a "$providers" -d 'Restrict or choose the provider'
complete -c starpoint -l force -d 'Replace existing configuration (init)'
complete -c starpoint -l context -r -F -d 'Override working directory'
complete -c starpoint -l verbose -s v -d 'List every effect of a change'
complete -c starpoint -l color -r -a 'auto always never' -d 'When to colorize output'
complete -c starpoint -l allow-destructive -d 'Let an unattended run delete or replace things'
complete -c starpoint -l allow-root -d 'Let a changing command run as root or elevated'
complete -c starpoint -l version -d 'Print version (install: pin a version)'
complete -c starpoint -l help -s h -d 'Show help'

# Per-command options
complete -c starpoint -n '__fish_seen_subcommand_from sync prune install add i remove uninstall rm transition move update upgrade' -l dry-run -d 'Preview; change nothing'
complete -c starpoint -n '__fish_seen_subcommand_from sync prune install add i remove uninstall rm transition move update upgrade recover' -l no-confirm -d 'Do not ask first'
complete -c starpoint -n '__fish_seen_subcommand_from list ls' -l managed -d 'Only entries with a recorded provider'
complete -c starpoint -n '__fish_seen_subcommand_from doctor transition move' -l all -d 'Show everything'
complete -c starpoint -n '__fish_seen_subcommand_from install add i' -l channel -r -d 'Release channel'
complete -c starpoint -n '__fish_seen_subcommand_from install add i' -l project -d 'Record in the project configuration'
complete -c starpoint -n '__fish_seen_subcommand_from install add i' -a '.' -d 'The current project'
complete -c starpoint -n '__fish_seen_subcommand_from transition move' -l reverse -d 'Reverse the last transition'
complete -c starpoint -n '__fish_seen_subcommand_from update upgrade' -l self -d 'Also update Starpoint'
complete -c starpoint -n '__fish_seen_subcommand_from runs history' -l package -r -d 'Only this package'
complete -c starpoint -n '__fish_seen_subcommand_from runs history' -l directory -r -F -d 'Only this directory'
complete -c starpoint -n '__fish_seen_subcommand_from recover' -l discard -d 'Remove leftover files'
complete -c starpoint -n '__fish_seen_subcommand_from remove uninstall rm' -l allow-empty -d 'Allow removing every configured package'
complete -c starpoint -n '__fish_seen_subcommand_from help' -a "$commands"

# Fish completion for Starpoint (str). Install with:
#   mkdir -p ~/.config/fish/completions
#   cp completions/str.fish completions/starpoint.fish ~/.config/fish/completions/
#
# Keep in sync with internal/cli/help.go (commandFlags); a test checks it.
set -l commands init sync list ls inventory adopt info show explain doctor install add i remove uninstall rm transition move update upgrade runs history prune recover logging logs help
set -l providers apt brew cargo dnf flatpak gem go nix npm pacman pip pnpm rustup snap system uv yarn

complete -c str -f
complete -c str -n "not __fish_seen_subcommand_from $commands" -a "$commands"

# Global options
complete -c str -l json -d 'Machine-readable output'
complete -c str -l provider -r -a "$providers" -d 'Restrict or choose the provider'
complete -c str -l force -d 'Replace existing configuration (init)'
complete -c str -l context -r -F -d 'Override working directory'
complete -c str -l verbose -s v -d 'List every effect of a change'
complete -c str -l color -r -a 'auto always never' -d 'When to colorize output'
complete -c str -l allow-destructive -d 'Let an unattended run delete or replace things'
complete -c str -l allow-root -d 'Let a changing command run as root or elevated'
complete -c str -l version -d 'Print version (install: pin a version)'
complete -c str -l help -s h -d 'Show help'

# Per-command options
complete -c str -n '__fish_seen_subcommand_from sync prune install add i remove uninstall rm transition move update upgrade adopt' -l dry-run -d 'Preview; change nothing'
complete -c str -n '__fish_seen_subcommand_from sync prune install add i remove uninstall rm transition move update upgrade recover adopt' -l no-confirm -d 'Do not ask first'
complete -c str -n '__fish_seen_subcommand_from list ls' -l managed -d 'Only entries with a recorded provider'
complete -c str -n '__fish_seen_subcommand_from doctor transition move' -l all -d 'Show everything'
complete -c str -n '__fish_seen_subcommand_from adopt' -l all -d 'Adopt everything installed on request'
complete -c str -n '__fish_seen_subcommand_from adopt' -a "$providers" -d 'Owner'
complete -c str -n '__fish_seen_subcommand_from inventory' -l format -r -a 'table toml' -d 'Output: table, or a TOML manifest'
complete -c str -n '__fish_seen_subcommand_from inventory' -l pin-versions -d 'Pin observed versions in the manifest'
complete -c str -n '__fish_seen_subcommand_from inventory' -l portability -d 'Estimate how reproducible the selected software is'
complete -c str -n '__fish_seen_subcommand_from install add i' -l channel -r -d 'Release channel'
complete -c str -n '__fish_seen_subcommand_from install add i' -l project -d 'Record in the project configuration'
complete -c str -n '__fish_seen_subcommand_from install add i' -l allow-duplicate -d 'Let an unattended install add a second copy'
complete -c str -n '__fish_seen_subcommand_from install add i' -a '.' -d 'The current project'
complete -c str -n '__fish_seen_subcommand_from transition move' -l reverse -d 'Reverse the last transition'
complete -c str -n '__fish_seen_subcommand_from update upgrade' -l self -d 'Also update Starpoint'
complete -c str -n '__fish_seen_subcommand_from runs history' -l package -r -d 'Only this package'
complete -c str -n '__fish_seen_subcommand_from runs history' -l directory -r -F -d 'Only this directory'
complete -c str -n '__fish_seen_subcommand_from recover' -l discard -d 'Remove leftover files'
complete -c str -n '__fish_seen_subcommand_from remove uninstall rm' -l allow-empty -d 'Allow removing every configured package'
complete -c str -n '__fish_seen_subcommand_from help' -a "$commands"

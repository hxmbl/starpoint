# Fish completion for starpoint, the old name of str (STRM-445). Fish loads
# completions by command name, so the old name needs its own file; it borrows
# every completion from str.fish rather than repeating them. Install it beside
# str.fish:
#   cp completions/str.fish completions/starpoint.fish ~/.config/fish/completions/
complete -c starpoint --wraps str

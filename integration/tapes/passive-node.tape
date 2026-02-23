# passive-node.tape
# VHS tape — records the passive node (validator-2) detecting a leaderless cluster
# and promoting itself to active. Output: docs/passive-node.gif
#
# The integration docker compose environment must be running before VHS starts.
# The Makefile `gif` target handles this automatically.

Output docs/passive-node.gif

Set FontSize 13
Set Width 1600
Set Height 500
Set Theme "Catppuccin Mocha"
Set PlaybackSpeed 2

Sleep 300ms

Hide
Type "bash integration/scripts/demo-passive-node.sh"
Enter
Show
# Real time for the script: ~28 s
#   3 s settle + 25 s log stream (covering 10 s steady + 12 s failover + 3 s post)
# Sleep here covers that plus a safety margin; PlaybackSpeed 2 halves the GIF duration.
Sleep 32s

# dtail: follow the logs of many servers at once, filtered by a regular
# expression. Ctrl-C shows the connection statistics, a second one exits.
Output dtail.gif
Source "_settings.tape"

Type "dtail --servers servers.txt --files 'logs/*.log' --regex 'status=50.|WARN' --no-interactive"
Enter
Sleep 6s
Ctrl+C
Sleep 2s
Ctrl+C
Sleep 2s

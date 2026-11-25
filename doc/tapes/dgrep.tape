# dgrep: search files of many servers with a regular expression, with
# context lines, a match limit and plain output; then a search through about
# 540 MB of gzip archives per server, whose read progress --progress writes to
# stderr while the matches go to the pipe.
Output dgrep.gif
Source "_settings.tape"

Type "dgrep --servers servers.txt --files logs/app.log --regex 'timeout' --max 1"
Enter
Sleep 3s
Type "dgrep --servers servers.txt --files logs/app.log --regex 'status=503' --before 1 --after 1 --max 1"
Enter
Sleep 4s
Type "dgrep --servers servers.txt --files logs/app.log --regex 'service=search.*status=500' --plain | wc -l"
Enter
Sleep 3s
Type "clear"
Enter
Type "dgrep --servers servers.txt --files 'logs/archive/*.log.gz' --regex 'status=503' \"
Enter
Type "  --plain --progress | wc -l"
Enter
Sleep 12s

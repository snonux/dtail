# dgrep: search files of many servers with a regular expression, with
# context lines, a match limit and plain output.
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

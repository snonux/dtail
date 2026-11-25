# dcat: print whole files of many servers at once. Here: which app version
# runs where, during a rollout; --progress reports on stderr what the
# servers read (here at once complete). Render with doc/tapes/render.sh.
Output dcat.gif
Source "_settings.tape"

Type "cat servers.txt"
Enter
Sleep 1.5s
Type "dcat --servers servers.txt --files version.txt"
Enter
Sleep 3s
Type "dcat --servers servers.txt --files version.txt --plain --progress | sort | uniq -c"
Enter
Sleep 4s

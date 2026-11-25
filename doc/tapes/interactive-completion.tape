# Tab completion in an interactive session: remote file paths inside
# --files, the field catalog (:fields), and a guided query after :query.
Output interactive-completion.gif
Source "_settings.tape"

Type "dtail --servers servers.txt --files logs/app.log --regex WARN"
Enter
Sleep 4s
Type ":reload --files logs/"
Tab
Sleep 2s
Type "d"
Tab
Sleep 1.5s
Enter
Sleep 3s
Type ":fields QUERIES"
Enter
Sleep 4s
# :fields keeps the output paused; an empty line resumes it.
Enter
Sleep 2s
Type ":query "
Tab
Sleep 1.5s
Tab
Sleep 1.5s
Tab
Sleep 2s
Type "table,sum(ro"
Tab
Sleep 1.5s
Type " group by table"
Sleep 1s
Enter
Sleep 5s
Type ":quit"
Enter
Sleep 1.5s

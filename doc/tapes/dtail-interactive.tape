# Interactive dtail (the default on a terminal): change the filter of the
# running session, switch it to a MapReduce query and back, without
# reconnecting.
Output dtail-interactive.gif
Source "_settings.tape"

Type "dtail --servers servers.txt --files logs/app.log --regex 'status=503'"
Enter
Sleep 5s
# Typing pauses the output until the line is entered.
Type ":reload --regex 'search.*status=50'"
Sleep 1.5s
Enter
Sleep 5s
Type ":query from REQUESTS select service,count(*),avg(latency) group by service interval 2"
Sleep 1.5s
Enter
Sleep 7s
Type ":tail"
Sleep 1s
Enter
Sleep 4s
Type ":show"
Sleep 1s
Enter
Sleep 3s
Type ":quit"
Enter
Sleep 1.5s

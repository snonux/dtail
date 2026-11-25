# dmap without --query opens the query builder: the servers are probed for
# the tables and fields of the files, Tab guides through the query step by
# step, and Enter runs it as --query would have.
Output dmap-query-builder.gif
Source "_settings.tape"

Type "dmap --servers servers.txt --files logs/app.log"
Sleep 500ms
Enter
Sleep 3s
# from, then the table
Tab
Sleep 1.5s
Type "REQ"
Tab
Sleep 1.5s
# select, then its items
Tab
Sleep 1.5s
Tab
Sleep 2.5s
Type "service,count(*),avg(lat"
Tab
Sleep 1.2s
Type ",max(lat"
Tab
Sleep 1.2s
# after a finished item Tab lists the next clauses
Tab
Sleep 2.5s
Type "group by "
Tab
Sleep 1.5s
Type "ser"
Tab
Sleep 1s
Type "order by avg(latency)"
Sleep 1.5s
Enter
Sleep 5s

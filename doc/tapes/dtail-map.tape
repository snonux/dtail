# dtail --query: a MapReduce query over the lines the servers write right
# now; the table is reported every interval (here 3 seconds).
Output dtail-map.gif
Source "_settings.tape"

Type "dtail --servers servers.txt --files logs/app.log --no-interactive \"
Enter
Type "  --query 'from REQUESTS select service,count(*),avg(latency),max(latency) group by service interval 3'"
Enter
Sleep 12s
Ctrl+C
Sleep 1s
Ctrl+C
Sleep 1.5s

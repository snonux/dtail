# dtail --query grouped by server: which host answers slowest right now.
Output dtail-map2.gif
Source "_settings.tape"

Type "dtail --servers servers.txt --files logs/app.log --no-interactive \"
Enter
Type "  --query 'from REQUESTS select $hostname,count(*),avg(latency),percentile(latency) group by $hostname order by avg(latency) interval 3'"
Enter
Sleep 12s
Ctrl+C
Sleep 1s
Ctrl+C
Sleep 1.5s

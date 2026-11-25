# dmap: a MapReduce query over the logs written so far, on every server,
# merged into one table; then the same query into a CSV outfile.
Output dmap.gif
Source "_settings.tape"

Type "dmap --servers servers.txt --files logs/app.log \"
Enter
Type "  --query 'from REQUESTS select $hostname,count(*),avg(latency),max(latency) where status >= 500 group by $hostname order by avg(latency)'"
Enter
Sleep 5s
Type "clear"
Enter
Type "dmap --servers servers.txt --files logs/app.log \"
Enter
Type "  --query 'from REQUESTS select service,count(*) where status >= 500 group by service outfile errors.csv'"
Enter
Sleep 4s
Type "cat errors.csv; cat errors.csv.query; echo"
Enter
Sleep 4s

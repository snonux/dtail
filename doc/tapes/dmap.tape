# dmap: a MapReduce query over the logs written so far, on every server,
# merged into one table; then the same query into a CSV outfile; then a
# query over about 540 MB of gzip archives per server, with the read progress
# (--progress, on stderr) and its ETA while the servers read.
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
Type "clear"
Enter
Type "dmap --servers servers.txt --files 'logs/archive/*.log.gz' --progress \"
Enter
Type "  --query 'from REQUESTS select service,count(*),avg(latency) where status >= 500 group by service'"
Enter
Sleep 16s

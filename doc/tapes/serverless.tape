# Serverless mode: without --servers the clients read local files (or
# stdin) themselves, no dserver and no SSH needed.
Output serverless.gif
Source "_settings.tape"

Type "dgrep --files logs/app.log --regex 'status=503' --max 3"
Enter
Sleep 3s
Type "seq 1 20 | dgrep --regex '^1' --plain | paste -sd' '"
Enter
Sleep 2.5s
Type "cat users.csv"
Enter
Sleep 2s
Type "dmap --files users.csv --query 'select team,count(*),avg(age),sum(logins) group by team logformat csv'"
Enter
Sleep 4s

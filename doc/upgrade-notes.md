DTail Upgrade Notes
===================

What changes for an existing installation when you upgrade to DTail 5.x,
newest first. For a new installation see the [Installation Guide](installation.md).

## Read progress of `dcat`, `dgrep` and `dmap`

dserver reports how far ``dcat``, ``dgrep`` and ``dmap`` read their files with a hidden ``.syn progress`` frame of protocol 5 (no capability, no version change).

* Clients from before it ignore the frame.
* With a dserver from before it a client shows no progress, and ``--progress`` prints a final line with ``reporting=0/N``.

See [examples.md](examples.md) for the progress lines.

## Paths holding a newline are no longer readable

dserver's read permission check denies every path that holds a newline character, in the file name or in a directory name above it (after symlinks are resolved), for every user the ``Server.Permissions`` rules apply to.

* Why: in a rule ``.`` matches no newline and ``$`` only the end of the path, so a deny rule such as ``!^/var/log/secret/.*$`` did not match such a path while an allow rule ``^/var/log/`` did, and the file was read past the deny rule.
* A read of such a path fails with ``read: no permission to read file`` (``dcat`` and ``dgrep`` then exit ``2``, also when only one file a glob matched has such a name). The path completion offers neither the file nor a directory with such a name.
* There is no option to turn this off; rename such files.
* dserver's own scheduled and continuous jobs, which the rules do not apply to, still read them.

## MapReduce table names are literal, `from *` reads every table

The line filter of a MapReduce query is built by the client (``dmap``, ``dtail --query``, and dserver for its ``Schedule`` and ``Continuous`` jobs), so this takes effect with the upgraded client or scheduling dserver:

* A table name is matched literally. It used to be put into a regular expression unescaped, which was never documented: ``from "(A|B)"`` read the tables ``A`` and ``B``, ``from STATS.*`` every table starting with ``STATS``, and ``from A.B`` also the table ``AXB``. Such a query now reads only the table of exactly that name (usually none). Use one query per table, or ``from *`` with a ``where`` condition.
* ``from *`` reads every ``MAPREDUCE`` line, whatever its table, as documented. It used to read only the lines of a table without a name (``|MAPREDUCE:|``), so a query using it returns more rows than before.

## MapReduce queries with stray words are rejected

Older releases silently ignored words the query parser did not expect after the value of a clause, so a misspelled or half-typed word changed nothing or the wrong thing (``order by x asc`` sorted descending, ``order by x reverse`` and ``limit 10 20`` ignored the last word). Such queries are now rejected with an error naming the word (see [the query language](querylanguage.md)). Rejected are:

* a word after the value of ``from``, ``order``, ``rorder``, ``interval``, ``limit``, ``logformat`` and ``outfile`` (e.g. ``order by x reverse``, ``limit 10 20``), including a second order field (``order by a,b``); a trailing ``desc`` after ``order`` and ``asc`` after ``rorder`` stay allowed;
* ``asc`` after ``order`` and ``desc`` after ``rorder``;
* a bare ``by`` in a ``select`` or ``group by`` list (``select count(x) gropu by x``);
* ``interval`` without a number;
* an ``outfile`` path starting with ``append:`` (``outfile append:result.csv`` wrote a new file named ``append:result.csv`` instead of appending to ``result.csv``);
* a path after a quoted or escaped ``append`` (``outfile "append" result.csv``, read as ``outfile append result.csv`` before): only the bare word ``append`` is the append mode, a quoted or escaped one is a file name.

Replacements:

* ``rorder by x`` for ascending order (instead of ``order by x asc`` or ``reverse``);
* a field named ``by`` is written ``` `by` ```;
* remove the stray word, or add the missing interval;
* ``outfile append result.csv`` to append, ``outfile ./append:result.csv`` for a file really named so.

### Check the job queries of dserver

dserver does not check the queries of the ``Schedule`` and ``Continuous`` jobs when it starts. A job whose query is now rejected fails each time it runs, with an ERROR log line ``Unable to create job <name>`` followed by the parse error, and writes no outfile. A scheduled job then backs off like any failed run and is given up 24 hours after its ``TimeRange`` ended; a continuous job logs the error at every retry, every minute.

Check the dserver log after the upgrade (the ``log`` directory is dserver's ``Common.LogDir``):

```console
% grep 'Unable to create job' log/*.log
```

Or check each job query with the new ``dmap --query '...'`` before upgrading dserver: it rejects such a query the same way before connecting.

## `outfile append` appends the result once

``dmap`` and ``dtail --query`` with ``outfile append PATH`` used to append the whole result so far to ``PATH`` at every report interval (every 5 seconds by default), so the file got the same rows again and again. They now append the result once, when the query ends (the client exits, or an interactive reload replaces the query).

* Scripts that read ``PATH`` while the query runs no longer see interim rows, and a client that is killed or crashes appends nothing.
* Use ``outfile PATH`` (without ``append``) for a file that is replaced with the result so far at every interval.
* Scheduled jobs (one final result per run) and continuous jobs (each interval's rows, once) are unchanged.

## `Common.Logger` and `Common.LogDir` now take effect

Older releases silently ignored ``Common.Logger`` (and the client commands' ``Common.LogDir``) from the config file, because every command's ``--logger`` and ``--logDir`` flag default always replaced it. These config values now apply. The precedence is:

1. an explicit ``--logger``/``--logDir`` flag,
2. ``Common.Logger``/``Common.LogDir`` from the config file,
3. the per-command default: ``file`` and ``log`` for dserver, ``fout`` and ``~/log`` for the client commands.

dtailhealth ignores both config values: it always uses the ``none`` logger unless given ``--logger``, and always the ``log`` directory. It previously honoured ``Common.LogDir`` when run with ``--logger file`` or ``--logger fout``; such log files now go to ``./log``.

Older copies of the example config contained ``"Logger": "Fout"``. If your installed ``/etc/dserver/dtail.json`` still has that line, dserver now tees its log to stdout as well as to the log file, so under systemd every line also lands in the journal. To keep the previous behaviour (log file only), set ``"Logger": "File"`` or remove the ``Logger`` key before restarting dserver after the upgrade:

```console
% grep -n '"Logger"' /etc/dserver/dtail.json
```

## `Client.TermColors` names now take effect

Older releases copied every ``Client.TermColors`` value verbatim into the output, so a colour name such as ``"Red"`` or ``"Dim"`` was printed as literal text instead of a colour; only raw escape sequences such as ``"\u001b[31m"`` worked. Colour and attribute names (matched case-insensitively) are now converted to terminal escape codes, so a configured palette now changes the client's colours.

Existing config files keep loading, dserver included (it reads the same file but never renders colours):

* The prefixed spellings used by the example configs of v4.0.0 through v4.3.4 (``samples/dtail.json.sample``, later ``examples/dtail.json.example``) (``"FgBlack"``, ``"BgCyan"``, ``"AttrDim"``, ``"AttrNone"`` ...) are still accepted and mean the colour or attribute they name. They are deprecated, and ``examples/dtail.schema.json`` reports them; drop the ``Fg``/``Bg``/``Attr`` prefix to silence the schema.
* Raw SGR escape sequences (``ESC[...m``, including several concatenated ones and ``:``-separated parameters) are used unchanged, as before, and ``""`` still means no escape code. Other raw strings that older releases passed through verbatim (for example ``"\u001b[K"`` or an escape code followed by text) are now treated like any other invalid value, see below.
* Any other value (for example a typo such as ``"Bleu"``) no longer ends up in the output. dtail uses that field's default colour instead and prints a warning naming the config file and the value on stderr, for example ``WARN: config file /etc/dserver/dtail.json: ignoring invalid background color "Bleu", using the default instead: ...``. A bad colour value is never a fatal config error.

## Protocol 5: upgrade clients and servers together

Clients and dserver must both speak protocol 5 (printed by ``--version``). Upgrade both sides together.

* A client refuses an older dserver: ``unsupported DTail server ... please update DTail server``, exit status 1.
* dserver rejects the commands of an older client with a version mismatch error (``... please update DTail client``). It keeps the session of a protocol 4.x or older client open until the client is stopped or ``IdleSessionTimeoutS`` passes, so that an older ``dtail`` does not reconnect in a loop.
* A 4.x ``dtailhealth`` session is ended at once, so the health check fails right away.

This also holds between dservers during a rolling upgrade. The scheduled and continuous jobs of a not yet upgraded 4.x dserver are 4.x clients: a job that reads from an already upgraded dserver gets the mismatch error and its session stays open until ``IdleSessionTimeoutS`` on the upgraded dserver. A released 4.x dserver (e.g. v4.3.4) runs its scheduled jobs one after another, so such a job holds up its other scheduled jobs meanwhile.

What protocol 5 changes for the output:

* **UTF-8 lines with `€`, `¬`, `Ь`**: every session escapes the protocol's 0xAC message delimiter byte in its frames, so such characters (whose UTF-8 encoding contains that byte) are transferred intact.
* **`--plain` lines starting with `.` or reading `AUTHKEY OK`**: dserver marks its control messages (hidden `.syn ...` messages, `AUTHKEY` replies), so `--plain` server-mode output keeps log lines such as `.hidden`, `.syn close connection` or `AUTHKEY OK` and never acts upon them.
* **File paths containing spaces**: dserver reads such paths (also globs, job ``Files`` and ``:reload --files``); the client sends them encoded. Commas always separate files (``--files a.log,b.log``, positional arguments and job ``Files`` are joined and split at commas), so paths containing a comma cannot be read, and blanks around a path are removed.

# Interactive Query Reload: control loop, line editor, completion

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

Interactive query control is opt-in on the client with `--interactive-query`,
except for `dtail`, where it is the default when stdin and stdout are a
terminal and the control TTY opens (`cli.InteractiveDefault` in
`internal/cli/interactive_default.go`; `--no-interactive` disables it and wins,
an explicit `--interactive-query[=false]` is kept). The logic lives in
`internal/cli` because the build compiles each command from its single
`main.go`. `dmap` without a query opens its query builder on a terminal (see
"dmap Query Builder"). Scheduled and continuous jobs are never interactive: `NewMaprClient`
forces it off for every mode but `DefaultMode`. `:help` prints per-mode
`:reload`, `:query` and `:tail` examples (grep filter, MapReduce query,
switching between them, files), each checked against
the real parser by `TestInteractiveHelpExamplesParse`.
On a terminal the control loop edits lines with `golang.org/x/term`'s
`Terminal` (`interactiveEditor`, `internal/clients/interactive_tty.go`) in a
cbreak mode that keeps ISIG/OPOST/ICRNL (`enterCbreakMode`,
`ttymode_*.go`); `term.MakeRaw` is not used, as it would disable Ctrl-C and
staircase the log output. Tab completion (`completeInteractive`,
`interactive_complete.go`) offers commands, the mode's flags from
`newInteractiveReloadFlagSet` (shared with the parser), the current
`--grep/--regex/--query/--files` value (not one holding a control or format
character, which the `:reload` tokenizer can not escape and x/term would
write raw: `currentValueCandidate` then gives `unprintableValueHint`
instead), and inside a `--query` value the
query language and field catalog (`completeQueryValue`,
`interactive_complete_query.go`: `splitInteractiveArgsPartial` maps the
decoded value back to raw offsets, `mapr.AnalyzePartial` says what is
expected, one candidate function per kind from the vocabulary tables and
`interactiveProber.catalogSnapshot`, candidates escaped for the open quote
with `escapeForQuote`; candidates holding a control or format character,
i.e. log content with escape sequences, are dropped for every kind at once
in `queryCandidateSource.groups` with `hasHiddenRune`, the path completion's rune
test; names are written as the tokenizer and parser need them,
`interactive_complete_query_words.go`: a keyword or `by` in backticks,
`order by` items with a blank in double quotes, and inside an open double
quote only the kinds `mapr.AnalyzePartial` expects there (`quotedExpect`),
closed by the quote; an empty catalog adds `queryCatalogHint`, or for a
session without files (`catalogSnapshot.NoFilesHint`) the no-files hint,
`catalogEmptyHint`). A session that reads no files at all
(`interactiveProber.readsNoFiles`, `internal/clients/interactive_nofiles.go`:
no file, and the `-` a serverless session without files gets counts as
reading only in serverless mode with stdin not a terminal, as
`readCommand.isInputFromPipe` reads it only then, while interactive `dtail`
is the default on a terminal) says `the session reads no files: :reload
--files PATH (Tab lists remote paths)` (`local paths` in serverless mode;
Tab note only with the line editor) after the banner (`writeControlBanner`),
on `:fields`, `:probe` and Tab after `:fields `, instead of waiting for
field data; no default files are made up. In such a session (`--files`
value empty or only `-`, `namesNoFile`) Tab right after `:reload --files `
has no current value to insert and completes the value not yet started
like an empty one, i.e. lists the working directory (`filesValueAt` in
`interactive_complete_paths.go`). The query
completion is guided (`interactive_complete_query_guide.go`): one step per
Tab in the order from -> table -> select -> items -> where -> group by ->
order by -> limit (`queryOutline.nextClauses`; a query begun with Tab
always starts with from: `from TABLE ` in one Tab when the catalog knows
one table of the format, `from ` with the tables listed in the same Tab
when it knows several, `completeFromFirst`, `queryCompletion.lead`, and
`from ` with `tableNameHint` while the catalog is empty; select first
when the servers answered without a table of the format, `tableless`,
e.g. csv or plain logs only generic parses; from as the only clause left
later is inserted with its one table too), a single next step inserted at
once;
clauses present on either side of the cursor are not offered again
(`newQueryOutline` runs `mapr.AnalyzeQuery` over the whole query: a word
left of the cursor plus the value's rest, `queryValueTail` for `--query`,
the line's rest for `:query`); after a finished item only the continuation
plus `commaItemHint`/`commaConditionHint` (also right after the `)` of a
finished item, `count(x)|`, or a where value ending in `)`: Tab lists
that, even one clause, and inserts the blank only at the query's end, never
before a blank, comma or word, `completeAfterItem`,
`queryCompletion.afterItem`/`lead`; a comma typed as the very next
key with the cursor right after that blank replaces it, `completion.itemBlank`,
`interactiveEditor.itemBlank`, `commaAfterItemBlank`, not in vi normal
mode), after a comma
(`mapr.Partial.AfterComma`) the items again; value positions offer no
fields; rare clauses (set, interval, outfile, logformat, rorder) only for a
typed prefix; fields, `$variables` (`catalogFormat.variablesOf`) and values
only of the query's table, wherever it is typed (`tableFirstHint` while
none is). Candidates come in labelled `candidateGroup`s
(`queryCompletion.groups`, `completion.groups`) with a heading of what is
expected (`queryHeading`: "field to count", "value for x", "next", ...);
no heading before the first clause (group `clause:`) or where the next
clauses stand beside other words; the editor prints them with
`interactiveStyle.listing` (`interactive_listing.go`): heading (dropped
when every group's label equals it), one line per label (groups of one
label merged, so `and` and the clauses are one `next:` line;
`aggregation:`, `field:`, `variable:`, fields before `$variables`), names
bare (`bareCandidateName` drops the `(`, `)`, quotes, protective
backticks inside quotes and blank Tab inserts, but keeps the backticks of
a keyword-named field such as `` `from` ``), wrapped to
`editorScreen.columns()` (`displayWidth`: East Asian wide runes count two
columns, from a small range table) and cut after
`listingMaxRows` rows with `... (N more, type to narrow)`, hints on their
own line; heading and labels in the theme's Heading style, names per kind,
plain text without colors. Commands, flags and paths have no groups and
are listed on one wrapped line. The query
completion reads the cache only, except that a typed `from` table not probed
on its own within 60 s has `completeEditorLine` ask for a table probe and
wait up to 1 s with the paused output held (see "Tab waits" below and
"Probe Command"). Inside a `--files`
value the editor completes remote paths (`completeEditorLine` in
`interactive_tty_paths.go` calls `completeFilesValue`,
`interactive_complete_paths.go`, before `completeInteractive`): the last
comma-separated entry's base name from the path listing of its directory
cached in the `fieldCatalog` (`storeListing`/`findListing`,
`interactive_catalog_paths.go`, 2 min, a listing that timed out or
failed 5 s and never for a narrower prefix, at most 64 listings, one server's
view), escaped for the open quote; names with a comma, edge blanks,
control or format characters (e.g. U+202E) are not offered; a listing's
error reason is hinted through `displayText`. After every
requested round (session start, `:reload`, `:probe`; not the periodic
ones, outside what `:probe` waits for) `run` prefetches the directories of
the session's files whose listing is not cached fresh (`:probe` refreshes
fresh ones too; never `journal:`, whose units are listed on Tab only, as
each listing runs `journalctl --field _SYSTEMD_UNIT`), without waiting for them (`prefetchListings`, `interactive_probe_paths.go`). An uncached listing is asked of the first
connection up (`interactiveProber.listPaths`, deduplicated, not bound to the
Tab's wait, so a late answer is cached for the next Tab) and waited for at
most `pathListingWait` (1 s) with the paused output held, not printed
(see "Tab waits hold the output" below; the output is always paused at
such a wait, as `editorKey` calls `startTyping` before it completes), also
in serverless mode, whose in-process server logs to the paused output; on
a timeout a hint is shown. A key that inserts text, Tab or a
history recall pauses the output (`interactivePause.pauseTyping`; x/term only
passes inserting and unbound keys to the callback, recalls are seen through
`controlHistory.onRecall`), the entered line resumes it silently; an explicit
Enter pause stays explicit. Starting a typing pause shows
`interactiveTypingHint` (Tab, `:help`, Enter) above the line (`startTyping`).
While the host key question is asked (`promptAnswers.prompting`, passed to
the editor as `interactiveEditor.prompting`) the line is its answer: keys
still pause, but no hint, Tab consumed without completing, and
`controlHistory.skip` keeps it out of the history. While the editor runs,
the Ctrl-C statistics go through its writer (`stats.setInterruptOutput`,
set by `openControlLines`), which redraws the edit line below them (to the
control TTY, also when stdout is redirected).
`:help` and `:fields` always leave the output paused (`keepPausedAfter`)
until the next empty line. A line started while the output is paused gets
the prompt `dtail> ` (`interactiveEditor.setPrompt`, set only between lines:
x/term draws a prompt at a line's start and counts it in every cursor move,
so changing it mid-line would misplace the cursor). With terminal colors on, the editor's control output is painted by
`interactiveStyle` (`internal/clients/interactive_style.go`, theme
`Client.TermColors.Interactive`, `config.InteractiveTermColors`): messages,
`:help`, `:show`, `:fields`, the prompt and the Tab candidates, grouped and
styled by `candidateKind` (`completion.kind`/`kinds`; query kinds come from
`queryCandidateKinds`). The style travels with the writer
(`editorWriter`, `styleOf`): a nil style (plain line reading, `--noColor`,
`--plain`, any other writer) returns every text unchanged. MapReduce
queries are highlighted by `queryHighlighter`
(`internal/clients/query_highlight.go`, token kinds of
`mapr.AnalyzePartial`, styles of the matching candidate kinds; stripped of
its SGR sequences the output equals the query, `quoted` equals
`strconv.Quote`): the query of `:show`, the echo `query: ...` after an
applied `:reload` (`reloadAppliedMessage`) and the raw query above the
result table (`clientOutputFormatter.PaintMaprRawQuery`, on the
`MaprTable.RawQuery*` colors as the base between tokens; only when stdout
is a terminal and no payload goes to a log file, `rawQueryTokenColors`,
else one single-color run so `grep` finds the query in piped output).
`paint` paints a text of several lines line by line;
`interactiveStyle.failure` first escapes the message's control and format
characters but the line feed (`terminalText`,
`internal/clients/interactive_terminal_text.go`), with and without colors,
as it can end in server text (the reason of a rejected update after `reload
failed:`/`query failed:`/`tail failed:`); those three pass `errorLines(err)`,
which gives every error of an `errors.Join` (also nested) a line of its own
and escapes the line feeds inside each (`terminalLine`), so a server's
reason can not forge a line of control output; `:fields` is built as typed lines
(`fieldsLines`, `fieldsLineKind`) and painted from them
(`interactiveStyle.fields`), never by reading its text again, while `:help`
is painted by its text, which is all constants of dtail
(`TestInteractiveHelpIsPaintedByKind`); the prompt is
unpainted on a terminal narrower than the painted prompt has runes
(`interactiveStyle.editorPrompt`: x/term cuts the prompt into rows by
runes, escape sequences included, and miscounts the cursor). Alt-E and Alt-V (fish's `edit_command_buffer` keys) open the line in
`$VISUAL`/`$EDITOR`/`vi` (`editExternally`,
`internal/clients/interactive_tty_external.go`): x/term reads ESC+letter as
one anonymous key, so `editInput` in front of the Terminal translates the
two sequences (outside bracketed paste) into `externalEditKey`
(`utf8.MaxRune`), which `editorKey` consumes; the editor runs via
`/bin/sh` on a temp file with a fresh descriptor of the TTY in the mode
dtail found it in (cbreak and the window size again after it; while
it runs, `editMu`/`editing`, the SIGCONT watcher sets no mode, a resize
redraws nothing, and a `close` (session end) leaves mode and cursor shape
to the editor, `endEditing` then setting nothing), output paused for
typing; its text (line breaks and control characters as blanks,
`editedLine`) replaces the line, which runs only on Enter; a
failing editor keeps the line. The editor's process dependencies (the
forced-exit hook and the environment naming the external editor) are an
`editorDeps` argument of `newInteractiveEditor`, passed down from
`runInteractiveControl`: `processEditorDeps` for a client run, tests pass
their own (`testEditorDeps`); `editorDeps.watchSignals` is the SIGCONT/
SIGWINCH watcher (`watchTTYSignals`), which tests leave out unless they
test the signals, as both go to the whole test process and would have
every other editor write to its PTY; a nil dependency is the process's
(`withDefaults`). The editor asks the terminal for bracketed
paste (`interactiveEditor.setPasteMode`, DECSET 2004: on at the start and
wherever cbreak mode is set again, i.e. after the external editor and
SIGCONT; off for the external editor, on close and, best effort in a
goroutine as a TTY write blocks on stopped output, on the forced exit;
never while dtail is in the background, `ttyMode.foreground`). x/term then inserts pasted keys without the key callback
(no Tab completion, a line break enters the line), so `editInput` does
what the callback and x/term do not: control and format characters of a
paste (`hiddenRune`, rune by rune, one cut by a read held back) but line
breaks become blanks (`pasteBlank`; x/term would put a tab on the
line raw and an ESC sequence as a private key code), a `Read` ends after
the first line break (`chunk`) so every pasted line gets a `Read` of its
own, and `onPaste` (`startTyping`) pauses the output before pasted text
that stays on the line is returned (`announcePaste`; a pasted line ending
in its line break runs at once and needs no pause). A paste marker cut by
a read is held back (`holdsTail`: always inside a paste and when more
than its ESC was read, else only when the read filled its buffer); a
start marker cut right after its ESC by a short read is put together by
x/term alone, without these three, and in vi mode that ESC is the Esc key,
so the marker's rest and the paste are normal mode keys (accepted); pasted
text holding the end marker ends the paste early. A blank line that was
pasted as a whole (`term.ErrPasteIndicator`) is skipped by
`interactiveEditor.readPastedOrTyped`, so it does not toggle the output
pause. `TestEditInputBracketedPaste`,
`TestInteractiveEditorBracketedPasteOnPTY`. `Client.EditMode` (`--edit-mode`, `emacs` default
or `vi`; checked by the clients only, `initializer.applyEditMode` in
`transformClient`, so a typo in a shared config stops no dserver) adds a vi layer
(`viEditor`, `internal/clients/interactive_vi.go`, motions in
`interactive_vi_motion.go`): x/term has no vi mode, so every line starts in
insert mode (x/term edits), `editInput` turns a lone ESC (ending the input read or
followed by ESC) into `viEscapeKey` (x/term would keep it as a sequence
start; reads are the Terminal's size, and a read that filled its buffer
holds a trailing ESC or partial paste marker for the next, so a backlog
cut by a read is not misread), and in normal mode `editorKey` hands every key but Tab to
`viEditor.key`, which returns the changed line (motions with counts,
d/c/y operators, x X D C s S Y, i a I A, r ~ p P, u/Ctrl-R per line, k/j
history via `controlHistory`). In vi mode `editInput` also turns Up and
Down (`ESC [ A`/`B`, and Ctrl-P/Ctrl-N, which x/term reads as these
arrows) into `viUpKey`/`viDownKey`, which `viEditor.arrowKey`
handles in both modes, so the arrows and k/j move one history position
(`viEditor.histIndex`; x/term's own never moves) and Down or j past the
newest entry restores the line being typed
(`TestViHistoryOnePosition`, through a real `term.Terminal`). The other
keys x/term handles itself (Enter, Backspace, Left/Right, Ctrl keys) never
reach it. Mode changes set the cursor
shape (DECSCUSR, `viCursorShapes`; default shape back on close). SIGCONT re-applies cbreak (bash/zsh leave the TTY
cooked after `fg`), SIGWINCH resizes; termios is only set while dtail is the
terminal's foreground process group (a background `tcsetattr` would stop it
with SIGTTOU). Lines of 4096+ runes (x/term's silent cut) are rejected
(`errControlLineTooLong`) and resume a typing pause. The mode is restored on every loop
exit and, via `signal.SetBeforeForceExit`, on the 5 s forced exit, whose
hook (`restoreForForcedExit`) only restores the mode: it waits neither for
the normal close nor for the SIGCONT/SIGWINCH watcher, which a resize can
block behind a `Terminal.Write` stuck on stopped output (Ctrl-S). Non-terminal
control TTYs keep the plain `bufio.Scanner` path.
The controlling TTY accepts `:reload <flags>`, `:query [query]` (dtail and
dmap), `:tail [regex]` (dtail), `:show`, `:fields [table]`,
`:probe`, `:help`, and `:quit`. `:query <query>` and `:tail [regex]`
(`internal/clients/interactive_query_command.go`) take the rest of the line
as typed, only the blanks around it dropped, with no shell quoting (the
query language has its own double quotes); the query must parse and the
regex compile before anything runs (rejected while still paused). In dtail
`:query` switches the running session to the query or replaces it
(`switchToQuery`), `:tail` switches back to the regex followed before and
`:tail <regex>` to that regex (`switchToTail`; `:tail` alone while
following lines is rejected); in dmap `:query` replaces the query like
`:reload --query` (`applyQueryCommand`); dgrep and dcat reject both, and
Tab offers them only where they exist (`interactiveCommandsFor`). `:query`
alone shows the running query. Both resume paused output first
(`resumesBeforeRun`), answer `query applied` (query echoed) or `following
lines` (regex), or `query failed:`/`tail failed:` leaving the workload as it
was. Tab after `:query ` runs the `--query` completer on the rest of the
line and inserts candidates without shell escaping (`completeQueryCommand`).
`:show` prints `workload=` (`query` while a query runs, else the mode).
`:fields` prints the field catalog of the
background prober (`fieldsText` in `internal/clients/interactive_fields.go`:
per log format its tables, fields with sample values, `$variables` and probe
notes, the answering servers, the data's age and the probe errors; a table
argument, matched ignoring case, narrows it to that table and its lines'
`$variables`, after a table probe of it, `awaitTableProbe`; server text is
Go-quoted unless plainly printable); Tab after `:fields ` completes the
catalog's tables.
`:probe` resumes the output like `:reload` (an answer is read by the handlers
the pause blocks), requests a round and waits up to 5 s for a round started
after the request (`interactiveProber.probeNow`, `probeRounds`), then reports
which servers answered, answered busy or failed, or that answers arriving
later are stored.
An empty line pauses the terminal output (client logger and serverless direct
output, `interactivePause`/`outputGate` in
`internal/clients/interactive_pause.go`) and prints the help; the next empty
line resumes it. The pause blocks the writers (backpressure to the servers, no
buffering, except the bounded hold of a Tab's wait, above); a `:reload` (or `:probe`, `:query`, `:tail`) that parsed resumes first, as its
acknowledgement is read by the blocked handlers, and the control loop resumes
when it ends; a rejected line (flags, quoting, query parse) is answered with
the output still paused (`resumesBeforeRun` is asked after
`parseInteractiveCommand`, which never logs). The periodic
client STATS are logged by a goroutine of their own (`stats.logStats`), so the
stats loop keeps taking Ctrl-C from `statsCh` while the logger is paused.
`:reload` flags are tokenized POSIX-shell-like without expansion
(`internal/clients/interactive_args.go`): single quotes are literal, double
quotes keep backslashes except `\\`, `\"`, `\$` and ``\` ``, and `''`/`""` is
an empty value (`--regex ''` clears the regex, so every line matches unless
`--invert` is in effect, which carries over between reloads and then matches
no line; `--invert=false` resets it, `:show` prints `invert=`). `--grep`
follows the CLI: a non-empty `--grep` wins over `--regex`; `--grep ''`
(ignored by the CLI) is rejected with a hint to use `--regex ''`. Blank `--files`, blank `--query`
values and lines that are not valid UTF-8 are rejected. Full rules:
`doc/interactive-query-reference.md` (short guide: `doc/interactive-query.md`).

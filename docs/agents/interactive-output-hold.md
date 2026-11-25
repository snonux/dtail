# Interactive Output Hold (Tab waits hold the output)

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

**Tab waits hold the output** (`interactivePause.holdOutput`,
`outputHold` in `internal/clients/interactive_hold.go`): a server's answer
to a path listing or table probe is read by the connection handlers that
write the output (with a remote dserver it is queued behind payload on the
same channel; the in-process server of serverless mode logs to the paused
output), so while Tab waits for one (`waitForListing`, 1 s) or `:fields
TABLE` for its table probe (`awaitTableProbe`, 2 s) the writers the pause
blocks must go on. They do, but nothing is printed: for the wait the paused
client logger offers what it would write to the hold instead of blocking
(`clientlog.Hold` -> `loggers.OutputHolder`, implemented by the `stdout`
logger next to its pause, delegated by `fout` and `DLog`; the daily file of
`fout` gets its lines at once), and so does the serverless direct output
(`outputGate.setHold`/`admit`). The hold is one ordered buffer of both, each
text with the writer it goes to, at most `outputHoldMaxBytes` (4 MiB); once
a text did not fit nothing more is taken and the writers block as while
paused (memory stays bounded; the Tab then gives up with `holdFullHint`
instead of "press Tab again": no answer can arrive until the output ran,
`missingAnswerHint`, `outputHold.isFull`). After
the wait the writers block again and the pause state (explicit or typing)
is what it was. `interactivePause.resume` has the held text written first:
`outputHold.release` queues it (one queue for every release, so text held
later never overtakes text held before) and a goroutine writes the queue
(`interactivePause.drain`, `outputHold.writeNext`: unchanged, colors
included, in the order taken); only when the queue is out it lets the
waiting writers go on (`blocked`, which outlasts `paused`). The drain
writes only while the output is not paused: it writes pieces of about
4 KiB ending at a line break (`holdPieceBytes`, `nextPiece`) and before
each waits for the resume of a pause that began meanwhile (`wanted`,
`resumed`), and `pause()` waits for the piece being written just then (at
most `holdPieceWait`, 200 ms, so a terminal taking no write does not hold
the control loop). So a key typed right behind the Enter, an explicit
pause or a Tab's wait stops the held text at once, at a line's end; what
is still queued stays queued, in order, ahead of what that pause's waits
hold, and its bytes count towards the bound
(`TestHoldDrainStopsForANewPause`, `TestHoldDrainWaitsWhilePausedAgain`,
`TestTypeAheadBehindEnterStopsHeldOutputOnPTY`). So the control loop never writes
held text itself and goes on reading lines when the output takes no
write (a pipe to a stopped reader): `:show` is answered and `:quit` taken
(`TestHoldDrainDoesNotBlockTheControlLoop`). Resumes are the entered line,
an empty line, `:reload`/`:probe`/`:query`/`:tail`, and every exit of the
control loop, which waits for the drain at most `holdDrainWait` (5 s,
`interactivePause.end`; the drain goes on behind it); text held before
`:help`/`:fields` kept the pause stays held until that pause ends. The
logger's text comes back through `loggers.heldOutput`, written past this
pause and flushed (so it keeps its place among serverless text). While
another pauser has the logger too (host key question, Ctrl-C statistics:
pause depth above one) that write is refused (an error whose `Paused`
method reports true) and the drain offers the piece again every 20 ms
(`holdBusyRetry`), looking at the pause each time; the whole queue,
serverless text included, waits behind it: all held text keeps one order
and none runs into what that pauser shows. The end of the control loop
first ends a host key question still asked (`answers.close()`, taken as a
no), so that it does not wait its 5 s for that pauser
(`TestHoldDrainKeepsOneOrderUnderAnotherPauser`, on the real terminal
logger, `loggers.NewTerminal`; `TestHoldDrainOrderStress`).
A `--log-payload` tee of serverless payload is behind the gate and gets
held payload at the release. Stdout redirected to a file or pipe is held
the same way (the pause blocks it as well). As nothing is printed during
the wait, the edit line stays on the screen: the former hide/restore of
the line around the wait (`editorScreen.hideLine`/`restoreLine`,
`interactiveEditor.hideLine`/`showLine`, the `hidden` state with its
deferred resize and raw statistics output) is gone; a resize or the Ctrl-C
statistics during a wait go through the Terminal as at any other time
(`TestInteractiveEditorDrawsDuringTabWaitOnPTY`; the Terminal writes the
echo of the keys of one read only after it handled them all, so keys read
together with a waiting Tab are echoed after the wait).
Both waits hold in both kinds of pause, typing and explicit; after
`:fields TABLE` in an explicit pause the lines stay held until the empty
line that resumes, which prints them before its `output resumed`.
`TestHoldOutputKeepsOneOrder`, `TestExplicitPauseHoldsOutputOfWaits`,
`TestStdoutHoldsOutputWhilePaused`,
`TestInteractiveEditorCompletesPathsOnPTY`,
`integrationtests/interactive_tab_hold_test.go` (dtail: Tab on a path
listing and on a table probe, `:fields TABLE`, typing and explicit pause;
dmap query builder: Tab after `from TABLE ` and `:fields TABLE`; dserver
and serverless).

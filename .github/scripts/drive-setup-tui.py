"""Drive the interactive `termp setup` wizard through a real Windows pseudo console.

`termp setup` only reaches its autostart step when stdin and stdout are a
terminal, so a plain CI step never exercises it (#472). This runs it under
ConPTY via pywinpty and accepts the defaults: Enter (apply), Enter (confirm
"Apply these settings?" = Yes), then Enter on the summary to quit.

usage: python drive-setup-tui.py <path-to-termp.exe>
Exits 0 only when the wizard reports "Setup applied." and termp exits 0.
"""

import re
import sys
import threading
import time

from winpty import PtyProcess

# CSI, OSC and two-byte escapes. ConPTY also replaces runs of spaces with
# cursor moves, so matching is done with all whitespace removed.
ANSI = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]")

chunks = []
lock = threading.Lock()


def transcript():
    with lock:
        return ANSI.sub("", "".join(chunks))


def squashed():
    return re.sub(r"\s+", "", transcript())


def reader(proc):
    while True:
        try:
            data = proc.read(4096)
        except EOFError:
            return
        with lock:
            chunks.append(data)


def fail(message):
    print(f"{message}\n--- transcript ---\n{transcript()}")
    sys.exit(1)


def wait_for(proc, needle, start, timeout=60):
    """Wait for needle to appear after offset start in the squashed transcript."""
    want = re.sub(r"\s+", "", needle)
    deadline = time.time() + timeout
    while time.time() < deadline:
        screen = squashed()
        if "setupfailed:" in screen[start:]:
            fail("setup reported a failure")
        index = screen.find(want, start)
        if index >= 0:
            return index + len(want)
        if not proc.isalive():
            fail(f"termp exited before showing {needle!r}")
        time.sleep(0.2)
    fail(f"timed out waiting for {needle!r}")


def main():
    # The transcript holds box-drawing glyphs the runner's cp1252 stdout cannot encode.
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    proc = PtyProcess.spawn([sys.argv[1], "setup"], dimensions=(40, 140))
    threading.Thread(target=reader, args=(proc,), daemon=True).start()

    offset = wait_for(proc, "enter to apply", 0)
    if "--toggle-first-row" in sys.argv[2:]:
        proc.write(" ")
        time.sleep(1)
    proc.write("\r")
    offset = wait_for(proc, "Apply these settings?", offset)
    proc.write("\r")
    wait_for(proc, "Setup applied.", offset, timeout=120)
    proc.write("\r")

    deadline = time.time() + 30
    while proc.isalive() and time.time() < deadline:
        time.sleep(0.2)
    if proc.isalive():
        proc.terminate(force=True)
        fail("termp setup did not exit after the summary")
    print(transcript())
    if proc.exitstatus != 0:
        fail(f"termp setup exited {proc.exitstatus}")


if __name__ == "__main__":
    main()

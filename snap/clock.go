package snap

import "time"

// The server clock: monotonic time since startup (like snapserver's steady_clock).
// Wall clock jumps (NTP sync, timezone change) can't touch it, so clients
// never see time go backwards and never glitch.
var start = time.Now()

func Now() time.Duration { return time.Since(start) + time.Second }

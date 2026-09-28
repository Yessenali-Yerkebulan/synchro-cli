package store

import "time"

// nowUTC is the single clock used for every timestamp we persist, so records
// sort correctly regardless of the machine's local timezone.
func nowUTC() time.Time { return time.Now().UTC() }
